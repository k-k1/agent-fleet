// runtime_kubernetes_types.go — hand-written Kubernetes API types, only the fields the
// kubernetes adapter writes or reads (ADR 0106 decision 10). A field missing here is
// dropped from what the adapter sends and ignored in what it reads, so every write is
// either a create of an object the adapter owns whole, or a JSON Patch of named paths:
// never a read-modify-PUT of an object someone else may have annotated.
package runtime

type kObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	DeletionTimestamp *string           `json:"deletionTimestamp,omitempty"`
}

type kLabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// --- StatefulSet ---

type kStatefulSet struct {
	APIVersion string             `json:"apiVersion,omitempty"`
	Kind       string             `json:"kind,omitempty"`
	Metadata   kObjectMeta        `json:"metadata"`
	Spec       kStatefulSetSpec   `json:"spec"`
	Status     kStatefulSetStatus `json:"status,omitempty"`
}

type kStatefulSetSpec struct {
	Replicas             *int32           `json:"replicas,omitempty"`
	Selector             *kLabelSelector  `json:"selector,omitempty"`
	ServiceName          string           `json:"serviceName,omitempty"`
	Template             kPodTemplateSpec `json:"template"`
	RevisionHistoryLimit *int32           `json:"revisionHistoryLimit,omitempty"`
}

type kStatefulSetStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	Replicas           int32  `json:"replicas"`
	ReadyReplicas      int32  `json:"readyReplicas,omitempty"`
	CurrentRevision    string `json:"currentRevision,omitempty"`
	UpdateRevision     string `json:"updateRevision,omitempty"`
}

// --- Pod ---

type kPodTemplateSpec struct {
	Metadata kObjectMeta `json:"metadata"`
	Spec     kPodSpec    `json:"spec"`
}

type kPod struct {
	Metadata kObjectMeta `json:"metadata"`
	Spec     kPodSpec    `json:"spec"`
	Status   kPodStatus  `json:"status,omitempty"`
}

type kPodList struct {
	Items []kPod `json:"items"`
}

type kPodSpec struct {
	ShareProcessNamespace         *bool                `json:"shareProcessNamespace,omitempty"`
	AutomountServiceAccountToken  *bool                `json:"automountServiceAccountToken,omitempty"`
	ServiceAccountName            string               `json:"serviceAccountName,omitempty"`
	EnableServiceLinks            *bool                `json:"enableServiceLinks,omitempty"`
	TerminationGracePeriodSeconds *int64               `json:"terminationGracePeriodSeconds,omitempty"`
	SecurityContext               *kPodSecurityContext `json:"securityContext,omitempty"`
	ImagePullSecrets              []kLocalObjectRef    `json:"imagePullSecrets,omitempty"`
	NodeSelector                  map[string]string    `json:"nodeSelector,omitempty"`
	NodeName                      string               `json:"nodeName,omitempty"`
	RestartPolicy                 string               `json:"restartPolicy,omitempty"`
	Containers                    []kContainer         `json:"containers"`
	Volumes                       []kVolume            `json:"volumes,omitempty"`
}

type kPodSecurityContext struct {
	RunAsNonRoot        *bool            `json:"runAsNonRoot,omitempty"`
	RunAsUser           *int64           `json:"runAsUser,omitempty"`
	RunAsGroup          *int64           `json:"runAsGroup,omitempty"`
	FSGroup             *int64           `json:"fsGroup,omitempty"`
	FSGroupChangePolicy string           `json:"fsGroupChangePolicy,omitempty"`
	SeccompProfile      *kSeccompProfile `json:"seccompProfile,omitempty"`
}

type kSeccompProfile struct {
	Type string `json:"type"`
}

type kLocalObjectRef struct {
	Name string `json:"name"`
}

type kContainer struct {
	Name            string                     `json:"name"`
	Image           string                     `json:"image"`
	ImagePullPolicy string                     `json:"imagePullPolicy,omitempty"`
	Ports           []kContainerPort           `json:"ports,omitempty"`
	Env             []kEnvVar                  `json:"env,omitempty"`
	EnvFrom         []kEnvFromSource           `json:"envFrom,omitempty"`
	Resources       kResources                 `json:"resources,omitempty"`
	VolumeMounts    []kVolumeMount             `json:"volumeMounts,omitempty"`
	ReadinessProbe  *kProbe                    `json:"readinessProbe,omitempty"`
	SecurityContext *kContainerSecurityContext `json:"securityContext,omitempty"`
}

type kContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int32  `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

type kEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type kEnvFromSource struct {
	SecretRef *kSecretEnvSource `json:"secretRef,omitempty"`
}

type kSecretEnvSource struct {
	Name string `json:"name"`
}

type kResources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type kVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
}

type kProbe struct {
	HTTPGet          *kHTTPGetAction `json:"httpGet,omitempty"`
	PeriodSeconds    int32           `json:"periodSeconds,omitempty"`
	TimeoutSeconds   int32           `json:"timeoutSeconds,omitempty"`
	FailureThreshold int32           `json:"failureThreshold,omitempty"`
}

type kHTTPGetAction struct {
	Path string `json:"path"`
	Port int32  `json:"port"`
}

type kContainerSecurityContext struct {
	AllowPrivilegeEscalation *bool            `json:"allowPrivilegeEscalation,omitempty"`
	Privileged               *bool            `json:"privileged,omitempty"`
	RunAsNonRoot             *bool            `json:"runAsNonRoot,omitempty"`
	Capabilities             *kCapabilities   `json:"capabilities,omitempty"`
	SeccompProfile           *kSeccompProfile `json:"seccompProfile,omitempty"`
}

type kCapabilities struct {
	Drop []string `json:"drop,omitempty"`
}

type kVolume struct {
	Name                  string                 `json:"name"`
	PersistentVolumeClaim *kPVCVolumeSource      `json:"persistentVolumeClaim,omitempty"`
	EmptyDir              *kEmptyDirVolumeSource `json:"emptyDir,omitempty"`
}

type kPVCVolumeSource struct {
	ClaimName string `json:"claimName"`
}

type kEmptyDirVolumeSource struct {
	SizeLimit string `json:"sizeLimit,omitempty"`
}

type kPodStatus struct {
	Phase             string             `json:"phase,omitempty"`
	Conditions        []kPodCondition    `json:"conditions,omitempty"`
	ContainerStatuses []kContainerStatus `json:"containerStatuses,omitempty"`
}

type kPodCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type kContainerStatus struct {
	Name         string          `json:"name"`
	Ready        bool            `json:"ready"`
	RestartCount int32           `json:"restartCount"`
	Image        string          `json:"image,omitempty"`
	State        kContainerState `json:"state,omitempty"`
}

type kContainerState struct {
	Waiting    *kContainerStateWaiting `json:"waiting,omitempty"`
	Running    *kContainerStateRunning `json:"running,omitempty"`
	Terminated *kContainerStateTerm    `json:"terminated,omitempty"`
}

type kContainerStateWaiting struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type kContainerStateRunning struct {
	StartedAt string `json:"startedAt,omitempty"`
}

type kContainerStateTerm struct {
	ExitCode int32  `json:"exitCode"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
}

// --- Service, Secret, PersistentVolumeClaim, PersistentVolume, Event ---

type kService struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   kObjectMeta  `json:"metadata"`
	Spec       kServiceSpec `json:"spec"`
}

type kServiceSpec struct {
	Type     string            `json:"type,omitempty"`
	Selector map[string]string `json:"selector,omitempty"`
	Ports    []kServicePort    `json:"ports,omitempty"`
}

type kServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"targetPort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

type kSecret struct {
	APIVersion string            `json:"apiVersion,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Metadata   kObjectMeta       `json:"metadata"`
	Type       string            `json:"type,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type kPVC struct {
	APIVersion string      `json:"apiVersion,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Metadata   kObjectMeta `json:"metadata"`
	Spec       kPVCSpec    `json:"spec"`
	Status     kPVCStatus  `json:"status,omitempty"`
}

type kPVCSpec struct {
	AccessModes      []string   `json:"accessModes,omitempty"`
	StorageClassName *string    `json:"storageClassName,omitempty"`
	Resources        kResources `json:"resources"`
	VolumeName       string     `json:"volumeName,omitempty"`
}

type kPVCStatus struct {
	Phase string `json:"phase,omitempty"`
}

type kPV struct {
	Metadata kObjectMeta `json:"metadata"`
}

type kEvent struct {
	Metadata       kObjectMeta `json:"metadata"`
	Type           string      `json:"type,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	Message        string      `json:"message,omitempty"`
	FirstTimestamp string      `json:"firstTimestamp,omitempty"`
	LastTimestamp  string      `json:"lastTimestamp,omitempty"`
	EventTime      string      `json:"eventTime,omitempty"`
}

type kEventList struct {
	Items []kEvent `json:"items"`
}
