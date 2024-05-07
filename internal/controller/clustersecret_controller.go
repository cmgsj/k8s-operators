package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cmgsj/k8s-operators/api/v1alpha1"
)

// ClusterSecretReconciler reconciles a ClusterSecret object
type ClusterSecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cmg.io,resources=clustersecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cmg.io,resources=clustersecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cmg.io,resources=clustersecrets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ClusterSecret{}).
		Owns(&corev1.Secret{}).
		Watches(&corev1.Namespace{}, r.watchNamespaces()).
		Complete(r)
}

// Reconcile reconciles a ClusterSecret object.
func (r *ClusterSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("reconciling object")

	clusterSecret := &v1alpha1.ClusterSecret{}
	err := r.Get(ctx, req.NamespacedName, clusterSecret)
	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to get object")
		return ctrl.Result{}, err
	}

	if !clusterSecret.DeletionTimestamp.IsZero() {
		log.Info("object is marked for deletion", "deletionTimestamp", clusterSecret.DeletionTimestamp)
		return ctrl.Result{}, nil
	}

	err = ValidateClusterSecret(clusterSecret)
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

func (r *ClusterSecretReconciler) applySecrets(ctx context.Context, clusterSecret *v1alpha1.ClusterSecret) ([]string, error) {
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

func (r *ClusterSecretReconciler) getClusterSecretNamespaces(ctx context.Context, clusterSecret *v1alpha1.ClusterSecret) ([]string, error) {
	selector, err := metav1.LabelSelectorAsSelector(clusterSecret.Spec.Namespaces.Selector)
	if err != nil {
		return nil, err
	}
	namespaceList := &corev1.NamespaceList{}
	err = r.List(ctx, namespaceList, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return nil, err
	}
	excludedNamespaces := make(map[string]struct{})
	for _, namespace := range clusterSecret.Spec.Namespaces.Exclude {
		excludedNamespaces[namespace] = struct{}{}
	}
	var namespaces []string
	for _, namespace := range namespaceList.Items {
		_, excluded := excludedNamespaces[namespace.Name]
		if !excluded {
			namespaces = append(namespaces, namespace.Name)
		}
	}
	return namespaces, nil
}

func (r *ClusterSecretReconciler) updateSecret(clusterSecret *v1alpha1.ClusterSecret, secret *corev1.Secret) error {
	secret.Type = clusterSecret.Spec.Type
	secret.Immutable = clusterSecret.Spec.Immutable
	secret.Data = clusterSecret.Spec.Data
	if metav1.GetControllerOf(secret) == nil {
		return controllerutil.SetControllerReference(clusterSecret, secret, r.Scheme)
	}
	return nil
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
	clusterSecretList := &v1alpha1.ClusterSecretList{}
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

var secretKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9-_\.]+$`)

func ValidateClusterSecret(clusterSecret *v1alpha1.ClusterSecret) error {
	var errs []error
	switch clusterSecret.Spec.Type {
	case "",
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
	size := 0
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