package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func (m *manager) TriggerPodMigration(pod *v1.Pod) (Result, error) {
	client, err := getHTTPClient()
	if err != nil {
		panic(err)
	}
	clonePod, err := m.kubeClient.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Spec.ClonePod, metav1.GetOptions{})
	if err != nil {
		return Result{}, err
	}

	containers := []string{}
	for _, c := range clonePod.Spec.Containers {
		containers = append(containers, c.Name)
	}

	// TODO(schrej): fetch port from api
	url := fmt.Sprintf("https://%s:10250/migrate/%s?containers=%s", clonePod.Status.HostIP, clonePod.GetUID(), strings.Join(containers, ","))
	response, err := client.Get(url)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("remote node answered with non-ok status code %v", response.StatusCode)
	}
	res := Result{}
	dec := json.NewDecoder(response.Body)
	if err := dec.Decode(&res); err != nil {
		return Result{}, err
	}

	time.Sleep(time.Second)

	return res, nil
}

func getHTTPClient() (*http.Client, error) {
	config, err := clientcmd.BuildConfigFromFlags("", "/var/lib/kubelet/kubeconfig")
	if err != nil {
		return nil, err
	}
	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		return nil, err
	}
	tlsConfig.InsecureSkipVerify = true //TODO REMOVE
	c := &http.Client{}
	c.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	return c, nil
}
