package migration

import (
	"net/http"
	"path"
	"strings"

	"github.com/emicklei/go-restful"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	klog "k8s.io/klog/v2"
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

func NewManager(kubeClient clientset.Interface, podManager kubepod.Manager, prepareMigartionFn prepareMigrationFunc, rootPath string) Manager {
	return &manager{
		migrationPath:      path.Join(rootPath, "migration"),
		kubeClient:         kubeClient,
		podManager:         podManager,
		prepareMigrationFn: prepareMigartionFn,
		migrations:         make(map[types.UID]*migration),
	}
}

type prepareMigrationFunc func(*v1.Pod)

type manager struct {
	migrationPath string

	kubeClient         clientset.Interface
	podManager         kubepod.Manager
	prepareMigrationFn prepareMigrationFunc

	migrations map[types.UID]*migration
}

var _ Manager = &manager{}

type migration struct {
	path       string
	containers []string
	unblock    chan struct{}
	done       chan struct{}
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
	klog.V(2).Infof("POST Migrate - %v %v", params.podUID, params.containerNames)

	var pod *v1.Pod
	var ok bool

	if pod, ok = m.podManager.GetPodByUID(types.UID(params.podUID)); !ok {
		res.WriteHeader(http.StatusNotFound)
		return
	}

	if pod.Status.Phase != v1.PodRunning {
		res.WriteHeader(http.StatusConflict)
		return
	}

	mig := m.newMigration(pod)
	mig.containers = params.containerNames

	klog.V(2).Infof("Starting migration of Pod %v", pod.Name)
	m.prepareMigrationFn(pod)

	<-mig.done
	r := Result{Containers: map[string]ResultContainer{}}
	for _, c := range mig.containers {
		r.Containers[c] = ResultContainer{CheckpointPath: path.Join(mig.path, c)}
	}
	if err := res.WriteAsJson(r); err != nil {
		klog.Error("failed to encode migration result.", err)
	}
	res.WriteHeader(http.StatusOK)
	mig.unblock <- struct{}{}
}

func (m *manager) FindMigrationForPod(pod *v1.Pod) (Migration, bool) {
	mig, ok := m.migrations[pod.UID]
	return mig, ok
}

func (m *manager) newMigration(pod *v1.Pod) *migration {
	mig := &migration{
		path:    path.Join(m.migrationPath, pod.Name),
		unblock: make(chan struct{}),
		done:    make(chan struct{}),
	}
	m.migrations[pod.GetUID()] = mig
	return mig
}

func (m *manager) removeMigration(pod *v1.Pod) {
	mig, ok := m.migrations[pod.GetUID()]
	if !ok {
		return
	}
	mig.done <- struct{}{}
	delete(m.migrations, pod.GetUID())
}

func (mg *migration) Options() *container.MigratePodOptions {
	return &container.MigratePodOptions{
		KeepRunning:    false,
		CheckpointsDir: mg.path,
		Unblock:        mg.unblock,
		Done:           mg.done,
		Containers:     mg.containers,
	}
}

func (mg *migration) WaitUntilFinished() {
	<-mg.unblock
}

type migrationRequestParams struct {
	podUID         string
	containerNames []string
}

func getMigrationRequestParams(req *restful.Request) migrationRequestParams {
	return migrationRequestParams{
		podUID:         req.PathParameter("podUID"),
		containerNames: strings.Split(req.QueryParameter("containers"), ","),
	}
}
