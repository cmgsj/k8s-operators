package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	k8soperatorsv1alpha1 "github.com/cmgsj/k8s-operators/api/v1alpha1"
)

// ClusterSecretReconciler reconciles a ClusterSecret object
type ClusterSecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=k8soperators.cmg.io,resources=clustersecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k8soperators.cmg.io,resources=clustersecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=k8soperators.cmg.io,resources=clustersecrets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&k8soperatorsv1alpha1.ClusterSecret{}).
		Owns(&corev1.Secret{}).
		Watches(&corev1.Namespace{}, r.watchNamespaces()).
		Complete(r)
}

// Reconcile reconciles a ClusterSecret object.
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("reconciling object")

	clusterSecret := &k8soperatorsv1alpha1.ClusterSecret{}
	err := r.Get(ctx, req.NamespacedName, clusterSecret)
	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to get object")
		return ctrl.Result{}, err
	}

	if !clusterSecret.DeletionTimestamp.IsZero() {
		log.Info("object is marked for deletion", "deletionTimestamp", clusterSecret.DeletionTimestamp)
		return ctrl.Result{}, nil
	}

	err = validateClusterSecret(clusterSecret)
	if err != nil {
		log.Error(err, "invalid object")
		return ctrl.Result{}, err
	}

	log.Info("applying secrets")

	namespaces, err := r.applySecrets(ctx, clusterSecret)
	if err != nil {
		log.Error(err, "failed to apply secrets")
		return ctrl.Result{}, err
	}

	log.Info("applied secrets")

	clusterSecret.Status.Namespaces = namespaces

	err = r.Status().Update(ctx, clusterSecret)
	if err != nil {
		log.Error(err, "failed to update object status")
		return ctrl.Result{}, err
	}

	log.Info("reconciled object")

	return ctrl.Result{}, nil
}

var secretKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9-_\.]+$`)

func validateClusterSecret(clusterSecret *k8soperatorsv1alpha1.ClusterSecret) error {
	var errs []error

	switch clusterSecret.Spec.Type {
	case "", // allow empty or omit ClusterSecret.Spec.Type
		corev1.SecretTypeOpaque,
		corev1.SecretTypeServiceAccountToken,
		corev1.SecretTypeDockercfg,
		corev1.SecretTypeDockerConfigJson,
		corev1.SecretTypeBasicAuth,
		corev1.SecretTypeSSHAuth,
		corev1.SecretTypeTLS,
		corev1.SecretTypeBootstrapToken:
	default:
		errs = append(errs, fmt.Errorf("invalid type %q", clusterSecret.Spec.Type))
	}

	var size int

	for key, value := range clusterSecret.Spec.Data {
		if !secretKeyRegex.MatchString(key) {
			errs = append(errs, fmt.Errorf("invalid data: key %q must match %s", key, secretKeyRegex))
		}

		size += len(value)
	}

	if size > corev1.MaxSecretSize {
		errs = append(errs, fmt.Errorf("invalid data: size %d must be at most %d", size, corev1.MaxSecretSize))
	}

	return errors.Join(errs...)
}

func (r *ClusterSecretReconciler) applySecrets(ctx context.Context, clusterSecret *k8soperatorsv1alpha1.ClusterSecret) ([]string, error) {
	namespaces, err := r.getClusterSecretNamespaces(ctx, clusterSecret)
	if err != nil {
		return nil, err
	}

	for _, namespace := range namespaces {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clusterSecret.Name,
			},
		}
		err = r.Get(ctx, client.ObjectKeyFromObject(secret), secret)
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = r.updateSecret(clusterSecret, secret)
				if err != nil {
					return nil, err
				}

				err = r.Create(ctx, secret)
				if err != nil {
					return nil, err
				}
			}

			return nil, err
		} else {
			if clusterSecret.Spec.Immutable != nil && *clusterSecret.Spec.Immutable {
				err = r.Delete(ctx, secret)
				if err != nil {
					return nil, err
				}

				err = r.updateSecret(clusterSecret, secret)
				if err != nil {
					return nil, err
				}

				err = r.Create(ctx, secret)
				if err != nil {
					return nil, err
				}
			} else {
				err = r.updateSecret(clusterSecret, secret)
				if err != nil {
					return nil, err
				}

				err = r.Update(ctx, secret)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	return namespaces, nil
}

func (r *ClusterSecretReconciler) updateSecret(clusterSecret *k8soperatorsv1alpha1.ClusterSecret, secret *corev1.Secret) error {
	secret.Type = clusterSecret.Spec.Type
	secret.Immutable = clusterSecret.Spec.Immutable
	secret.Data = clusterSecret.Spec.Data

	if metav1.GetControllerOf(secret) == nil {
		return controllerutil.SetControllerReference(clusterSecret, secret, r.Scheme)
	}

	return nil
}

func (r *ClusterSecretReconciler) getClusterSecretNamespaces(ctx context.Context, clusterSecret *k8soperatorsv1alpha1.ClusterSecret) ([]string, error) {
	includeNamespace, err := namespaceRuleMatcher(clusterSecret.Spec.Namespaces.Include)
	if err != nil {
		return nil, err
	}

	excludeNamespace, err := namespaceRuleMatcher(clusterSecret.Spec.Namespaces.Exclude)
	if err != nil {
		return nil, err
	}

	namespaceList := &corev1.NamespaceList{}
	err = r.List(ctx, namespaceList)
	if err != nil {
		return nil, err
	}

	var namespaceNames []string

	for _, namespace := range namespaceList.Items {
		if includeNamespace(namespace) && !excludeNamespace(namespace) {
			namespaceNames = append(namespaceNames, namespace.GetName())
		}
	}

	return namespaceNames, nil
}

func namespaceRuleMatcher(rule k8soperatorsv1alpha1.ClusterSecretNamespaceRule) (func(corev1.Namespace) bool, error) {
	nameSet := make(map[string]struct{}, 0)

	for _, name := range rule.Names {
		nameSet[name] = struct{}{}
	}

	var nameRegexp *regexp.Regexp

	if rule.Regexp != nil {
		regex, err := regexp.Compile(*rule.Regexp)
		if err != nil {
			return nil, err
		}
		nameRegexp = regex
	}

	labelSelector, err := metav1.LabelSelectorAsSelector(rule.Selector)
	if err != nil {
		return nil, err
	}

	return func(namespace corev1.Namespace) bool {
		_, match := nameSet[namespace.GetName()]
		if match {
			return true
		}

		if nameRegexp != nil && nameRegexp.MatchString(namespace.GetName()) {
			return true
		}

		return labelSelector.Matches(labels.Set(namespace.GetLabels()))
	}, nil
}

func (r *ClusterSecretReconciler) watchNamespaces() handler.EventHandler {
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			for _, req := range r.mapNamespaceToRequests(ctx, e.Object) {
				q.Add(req)
			}
		},
	}
}

func (r *ClusterSecretReconciler) mapNamespaceToRequests(ctx context.Context, object client.Object) []ctrl.Request {
	_, isNamespace := object.(*corev1.Namespace)
	if !isNamespace {
		return nil
	}

	clusterSecretList := &k8soperatorsv1alpha1.ClusterSecretList{}
	err := r.List(ctx, clusterSecretList)
	if err != nil {
		return nil
	}

	requests := make([]ctrl.Request, len(clusterSecretList.Items))

	for i, clusterSecret := range clusterSecretList.Items {
		requests[i] = ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: clusterSecret.Namespace,
				Name:      clusterSecret.Name,
			},
		}
	}

	return requests
}
