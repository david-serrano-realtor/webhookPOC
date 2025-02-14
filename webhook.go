package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error creating in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %v", err)
	}

	// Set up the HTTP handler.
	http.HandleFunc("/mutate", func(w http.ResponseWriter, r *http.Request) {
		serveMutate(w, r, clientset)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8443"
	}
	log.Printf("Starting webhook server on port %s", port)

	// TLS cert/key should be mounted at /tls/tls.crt and /tls/tls.key.
	log.Fatal(http.ListenAndServeTLS(":"+port, "/tls/tls.crt", "/tls/tls.key", nil))
}

// serveMutate handles the AdmissionReview request.
func serveMutate(w http.ResponseWriter, r *http.Request, clientset *kubernetes.Clientset) {
	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil || len(body) == 0 {
		writeAdmissionError(w, http.StatusBadRequest, "Empty request body")
		return
	}

	var reviewReq admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &reviewReq); err != nil {
		writeAdmissionError(w, http.StatusBadRequest, "Could not unmarshal AdmissionReview")
		return
	}

	// Call the mutation logic, which returns an AdmissionResponse.
	response := mutate(&reviewReq, clientset)
	response.UID = reviewReq.Request.UID

	// Wrap the response in an AdmissionReview with TypeMeta.
	reviewResp := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: response,
	}

	respBytes, err := json.Marshal(reviewResp)
	if err != nil {
		writeAdmissionError(w, http.StatusInternalServerError, "Could not marshal AdmissionReview response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

// mutate checks for the target label and builds a JSON patch.
func mutate(ar *admissionv1.AdmissionReview, clientset *kubernetes.Clientset) *admissionv1.AdmissionResponse {
	req := ar.Request

	// Only handle Pod objects.
	if req.Kind.Kind != "Pod" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result:  &metav1.Status{Message: "Could not unmarshal Pod: " + err.Error()},
		}
	}

	// Check for a label key starting with "rollouts-pod-template-hash".
	found := false
	for key := range pod.Labels {
		if strings.HasPrefix(key, "rollouts-pod-template-hash") {
			found = true
			break
		}
	}

	if !found {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	// Retrieve labels from the external API (MOCK)
	labels, err := getLabelsFromAPI()
	if err != nil {
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result:  &metav1.Status{Message: "Error retrieving labels from API: " + err.Error()},
		}
	}

	// Build the JSON patch.
	var patches []map[string]interface{}
	if pod.Labels == nil {
		patches = append(patches, map[string]interface{}{
			"op":    "add",
			"path":  "/metadata/labels",
			"value": map[string]string{},
		})
	}

	for key, value := range labels {
		op := "add"
		if pod.Labels != nil {
			if _, exists := pod.Labels[key]; exists {
				op = "replace"
			}
		}
		patches = append(patches, map[string]interface{}{
			"op":    op,
			"path":  "/metadata/labels/" + escapeJSONPointer(key),
			"value": value,
		})
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		return &admissionv1.AdmissionResponse{
			Allowed: false,
			Result:  &metav1.Status{Message: "Could not marshal JSON patch: " + err.Error()},
		}
	}

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

// writeAdmissionError returns a valid AdmissionReview with an error status.
func writeAdmissionError(w http.ResponseWriter, code int, message string) {
	w.WriteHeader(code)

	errResp := admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: message,
		},
	}

	reviewResp := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &errResp,
	}

	respBytes, _ := json.Marshal(reviewResp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

// escapeJSONPointer escapes characters for a JSON patch path.
func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

// getLabelsFromAPI mocks an API call and returns labels.
func getLabelsFromAPI() (map[string]string, error) {
	return map[string]string{"team": "microservices"}, nil
}
