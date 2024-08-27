package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type ServerParameters struct {
	port     int
	certFile string
	keyFile  string
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

var (
	parameters            ServerParameters
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
	config                *rest.Config
	clientSet             *kubernetes.Clientset
)

func main() {

	useKubeConfig := os.Getenv("USE_KUBECONFIG")
	kubeConfigFilePath := os.Getenv("KUBECONFIG")

	flag.IntVar(&parameters.port, "port", 8443, "Webhook server port.")
	flag.StringVar(&parameters.certFile, "tlsCertFile", "/etc/webhook/certs/tls.crt", "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&parameters.keyFile, "tlsKeyFile", "/etc/webhook/certs/tls.key", "File containing the x509 private key to --tlsCertFile.")
	flag.Parse()

	if len(useKubeConfig) == 0 {
		c, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		config = c
	} else {
		var kubeconfig string
		if kubeConfigFilePath == "" {
			if home := homedir.HomeDir(); home != "" {
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
		} else {
			kubeconfig = kubeConfigFilePath
		}
		fmt.Println("kubeconfig: " + kubeconfig)
		c, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			panic(err.Error())
		}
		config = c
	}

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	clientSet = cs
	test()
	http.HandleFunc("/", HandleRoot)
	http.HandleFunc("/mutate", HandleMutate)
	log.Fatal(http.ListenAndServeTLS(":"+strconv.Itoa(parameters.port), parameters.certFile, parameters.keyFile, nil))
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("RootHandler!"))
}

func HandleMutate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	log.Printf("Request Body: %s", string(body))
	if err != nil {
		panic(err.Error())
	}
	var admissionReviewReq admissionv1.AdmissionReview
	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReviewReq); err != nil {
		http.Error(w, fmt.Sprintf("could not deserialize request: %v", err), http.StatusBadRequest)
		return
	} else if admissionReviewReq.Request == nil {
		http.Error(w, "malformed admission review: request is nil", http.StatusBadRequest)
		return
	}

	fmt.Printf("Received AdmissionReview for Kind: %v, Namespace: %v, Name: %v, UID: %v\n",
		admissionReviewReq.Request.Kind, admissionReviewReq.Request.Namespace,
		admissionReviewReq.Request.Name, admissionReviewReq.Request.UID)

	var pod corev1.Pod

	if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &pod); err != nil {
		http.Error(w, fmt.Sprintf("could not unmarshal pod on admission request: %v", err), http.StatusBadRequest)
		fmt.Printf("Error unmarshaling Pod object: %v\n", err)
		return
	}

	fmt.Printf("Pod Name: %s, Namespace: %s\n", pod.Name, pod.Namespace)

	privateRegistry := os.Getenv("PRIVATE_REGISTRY")
	publicProject := os.Getenv("PUBLIC_PROJECT")
	if privateRegistry == "" || publicProject == "" {
		http.Error(w, "private registry or public project environment variables are not set", http.StatusInternalServerError)
		fmt.Println("Environment variables PRIVATE_REGISTRY or PUBLIC_PROJECT are not set.")
		return
	}

	var patches []patchOperation

	mutateImageName := func(image string) string {
		if strings.HasPrefix(image, privateRegistry) {
			return image
		}

		if atIndex := strings.Index(image, "@sha256:"); atIndex != -1 {
			image = image[:atIndex]
		}

		return fmt.Sprintf("%s/%s/%s", privateRegistry, publicProject, image)
	}

	for i, container := range pod.Spec.Containers {
		originalImage := container.Image
		if !strings.HasPrefix(originalImage, privateRegistry) {
			newImage := mutateImageName(originalImage)

			fmt.Printf("Mutating container %d: %s -> %s\n", i, originalImage, newImage)

			patches = append(patches, patchOperation{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/containers/%d/image", i),
				Value: newImage,
			})
		}
	}

	for i, initContainer := range pod.Spec.InitContainers {
		originalImage := initContainer.Image

		if !strings.HasPrefix(originalImage, privateRegistry) {
			newImage := mutateImageName(originalImage)

			fmt.Printf("Mutating initContainer %d: %s -> %s\n", i, originalImage, newImage)

			patches = append(patches, patchOperation{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/initContainers/%d/image", i),
				Value: newImage,
			})
		}
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not marshal JSON patch: %v", err), http.StatusInternalServerError)
		fmt.Printf("Error marshaling JSON patch: %v\n", err)
		return
	}

	fmt.Printf("JSON Patch: %s\n", string(patchBytes))

	admissionReviewResponse := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: &admissionv1.AdmissionResponse{
			UID:     admissionReviewReq.Request.UID,
			Allowed: true,
			Patch:   patchBytes,
			PatchType: func() *admissionv1.PatchType {
				pt := admissionv1.PatchTypeJSONPatch
				return &pt
			}(),
		},
	}

	responseBytes, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshaling response: %v", err), http.StatusInternalServerError)
		return
	}

	fmt.Printf("AdmissionReview Response: %s\n", string(responseBytes))

	w.Header().Set("Content-Type", "application/json")
	w.Write(responseBytes)
}
