package webhook

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog"
	"net/http"
	"strings"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()
)

func init() {
	corev1.AddToScheme(runtimeScheme)
	admissionregistrationv1beta1.AddToScheme(runtimeScheme)
	v1beta1.AddToScheme(runtimeScheme)
}

type WebhookServer struct {
	// CustomDnsPolicyConfig *CustomDnsPolicyConfig
	Server *http.Server
}

type CustomDnsPolicyConfig struct {
	DnsPolicy corev1.DNSPolicy    `yaml:"dnsPolicy"`
	DnsConfig corev1.PodDNSConfig `yaml:"dnsConfig"`
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

var ignoredNamespaces = []string{
	metav1.NamespaceSystem,
	metav1.NamespacePublic,
}

const (
	admissionWebhookAnnotationInjectKey = "localdns-policy-webhook/inject"
	admissionWebhookAnnotationStatusKey = "localdns-policy-webhook/status"
)

func LoadCustomDnsConfig(namespace string) *CustomDnsPolicyConfig {
	nsSvc := fmt.Sprintf("%s.svc.cluster.local", namespace)
	ndots := "2"
	opt := corev1.PodDNSConfigOption{
		Name:  "ndots",
		Value: &ndots,
	}
	return &CustomDnsPolicyConfig{
		DnsPolicy: "None",
		DnsConfig: corev1.PodDNSConfig{
			Nameservers: []string{"169.254.20.10"},
			Searches:    []string{nsSvc, "svc.cluster.local", "cluster.local"},
			Options: []corev1.PodDNSConfigOption{
				opt,
			},
		},
	}
}

// Check whether the target resoured need to be mutated
func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernete system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			klog.V(1).Infof("Skip mutation for %v for it' in special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}

	annotations := metadata.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	status := annotations[admissionWebhookAnnotationStatusKey]

	// determine whether to perform mutation based on annotation for the target resource
	var required bool
	if strings.ToLower(status) == "injected" {
		required = false
	} else {
		switch strings.ToLower(annotations[admissionWebhookAnnotationInjectKey]) {
		default:
			required = false
		case "y", "yes", "true", "on":
			required = true
		}
	}

	klog.Infof("Mutation policy for %v/%v: status: %q required:%v", metadata.Namespace, metadata.Name, status, required)
	return required
}

func (whserver *WebhookServer) Serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}

	if len(body) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		klog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = whserver.mutate(&ar)
	}

	response := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		response.Response = admissionResponse
		if ar.Request != nil {
			response.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(response)
	if err != nil {
		klog.Errorf("Could not encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
		return
	}
	klog.Infof("ready to write response ...")
	if _, err := w.Write(resp); err != nil {
		klog.Errorf("can not write http response: %v", err)
		http.Error(w, fmt.Sprintf("can not write http response: %v", err), http.StatusInternalServerError)
	}
}

func (whserver *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		klog.Errorf("Could not unmarshal raw object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	klog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pod.Name, req.UID, req.Operation, req.UserInfo)

	// determine whether to perform mutation
	if !mutationRequired(ignoredNamespaces, &pod.ObjectMeta) {
		klog.Infof("Skipping mutation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	annotations := map[string]string{admissionWebhookAnnotationStatusKey: "injected"}

	// get pod namespace
	podNamespace := pod.Namespace
	if podNamespace == "" {
		podNamespace = "default"
	}
	customDpc := LoadCustomDnsConfig(podNamespace)
	patchBytes, err := createPatch(&pod, customDpc, annotations)

	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	klog.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

func createPatch(pod *corev1.Pod, config *CustomDnsPolicyConfig, annotations map[string]string) ([]byte, error) {
	var patch []patchOperation

	// update dns policy
	patch = append(patch, updateDnsPolicy(config.DnsPolicy)...)

	patch = append(patch, updateDnsConfig(pod.Spec.DNSConfig, config.DnsConfig)...)
	// update annotations
	patch = append(patch, updateAnnotation(pod.Annotations, annotations)...)

	return json.Marshal(patch)
}

func updateDnsPolicy(update corev1.DNSPolicy) (patch []patchOperation) {
	patch = append(patch, patchOperation{
		Op:    "replace",
		Path:  "/spec/dnsPolicy",
		Value: update,
	})

	return patch
}

func updateDnsConfig(target *corev1.PodDNSConfig, update corev1.PodDNSConfig) (patch []patchOperation) {
	if target == nil {
		patch = append(patch, patchOperation{
			Op:    "add",
			Path:  "/spec/dnsConfig",
			Value: update,
		})
	} else {
		patch = append(patch, patchOperation{
			Op:    "replace",
			Path:  "/spec/dnsConfig",
			Value: update,
		})
	}

	return patch
}

func updateAnnotation(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		if target == nil || target[key] == "" {
			target = map[string]string{}
			patch = append(patch, patchOperation{
				Op:   "add",
				Path: "/metadata/annotations",
				Value: map[string]string{
					key: value,
				},
			})
		} else {
			patch = append(patch, patchOperation{
				Op:    "replace",
				Path:  "/metadata/annotations/" + key,
				Value: value,
			})
		}
	}
	return patch
}
