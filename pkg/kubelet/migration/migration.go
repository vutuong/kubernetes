package migration

import (
	"net/http"

	"github.com/emicklei/go-restful"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/container"
	kubepod "k8s.io/kubernetes/pkg/kubelet/pod"
)

type Manager interface {
	HandleMigrationRequest(*restful.Request, *restful.Response)
	FindMigrationForPod(*v1.Pod) (Migration, bool)
	TriggerPodMigration(*v1.Pod) (Result, error)
}

type Migration interface {
	Options() *container.MigratePodOptions
	WaitUntilFinished()
}

func NewManager(kubeClient clientset.Interface, podManager kubepod.Manager, prepareMigartionFn prepareMigrationFunc) Manager {
	return &manager{
		kubeClient:         kubeClient,
		podManager:         podManager,
		prepareMigrationFn: prepareMigartionFn,
		migrations:         make(map[types.UID]*migration),
	}
}

type prepareMigrationFunc func(*v1.Pod)

type manager struct {
	kubeClient         clientset.Interface
	podManager         kubepod.Manager
	prepareMigrationFn prepareMigrationFunc

	migrations map[types.UID]*migration
}

var _ Manager = &manager{}

type migration struct {
	containers []string
	unblock    chan struct{}
	created    chan string
}

type Result struct {
	Containers map[string]ResultContainer
}

type ResultContainer struct {
	CheckpointPath string
}

var _ Migration = &migration{}

func (m *manager) HandleMigrationRequest(req *restful.Request, res *restful.Response) {
	params := getMigrationRequestParams(req)
	klog.V(2).Infof("POST Migrate - %v %v %v", params.podNamespace, params.podID, params.containerName)

	var pod *v1.Pod
	var ok bool
	if pod, ok = m.podManager.GetPodByName(params.podNamespace, params.podID); !ok {
		res.WriteHeader(http.StatusNotFound)
		return
	}

	if pod.Status.Phase != v1.PodRunning {
		res.WriteHeader(http.StatusConflict)
		return
	}

	mig := m.newMigration(pod)
	mig.containers = []string{params.containerName}

	klog.V(2).Infof("Starting migration of Pod %v", pod.Name)
	m.prepareMigrationFn(pod)

	r := Result{Containers: map[string]ResultContainer{params.containerName: {CheckpointPath: <-mig.created}}}
	res.WriteAsJson(r)
	res.WriteHeader(http.StatusOK)
	mig.unblock <- struct{}{}
}

func (m *manager) FindMigrationForPod(pod *v1.Pod) (Migration, bool) {
	mig, ok := m.migrations[pod.UID]
	return mig, ok
}

func (m *manager) newMigration(pod *v1.Pod) *migration {
	mig := &migration{
		unblock: make(chan struct{}),
		created: make(chan string),
	}
	m.migrations[pod.UID] = mig
	return mig
}

func (mg *migration) Options() *container.MigratePodOptions {
	return &container.MigratePodOptions{
		KeepRunning:    false,
		CheckpointPath: mg.created,
		Unblock:        mg.unblock,
		Containers:     mg.containers,
	}
}

func (mg *migration) WaitUntilFinished() {
	<-mg.unblock
}

type migrationRequestParams struct {
	podNamespace  string
	podID         string
	containerName string
}

func getMigrationRequestParams(req *restful.Request) migrationRequestParams {
	return migrationRequestParams{
		podNamespace:  req.PathParameter("podNamespace"),
		podID:         req.PathParameter("podID"),
		containerName: req.PathParameter("containerName"),
	}
}
