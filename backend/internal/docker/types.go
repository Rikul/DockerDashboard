package docker

// Only the fields the dashboard reads are declared; the daemon sends more.

type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
}

type Info struct {
	ID                string `json:"ID"`
	Name              string `json:"Name"`
	ServerVersion     string `json:"ServerVersion"`
	OperatingSystem   string `json:"OperatingSystem"`
	Architecture      string `json:"Architecture"`
	NCPU              int    `json:"NCPU"`
	MemTotal          int64  `json:"MemTotal"`
	Containers        int    `json:"Containers"`
	ContainersRunning int    `json:"ContainersRunning"`
	ContainersPaused  int    `json:"ContainersPaused"`
	ContainersStopped int    `json:"ContainersStopped"`
	Images            int    `json:"Images"`
	Driver            string `json:"Driver"`
}

type Port struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type EndpointSettings struct {
	NetworkID  string `json:"NetworkID"`
	IPAddress  string `json:"IPAddress"`
	MacAddress string `json:"MacAddress"`
}

// Container is an entry of GET /containers/json.
type Container struct {
	ID              string            `json:"Id"`
	Names           []string          `json:"Names"`
	Image           string            `json:"Image"`
	ImageID         string            `json:"ImageID"`
	Command         string            `json:"Command"`
	Created         int64             `json:"Created"`
	State           string            `json:"State"`  // created, running, paused, restarting, removing, exited, dead
	Status          string            `json:"Status"` // e.g. "Up 2 hours (healthy)"
	Ports           []Port            `json:"Ports"`
	Labels          map[string]string `json:"Labels"`
	Mounts          []Mount           `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]EndpointSettings `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ContainerJSON is GET /containers/{id}/json, trimmed.
type ContainerJSON struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Created      string `json:"Created"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health,omitempty"`
	} `json:"State"`
	Config struct {
		Tty   bool   `json:"Tty"`
		Image string `json:"Image"`
	} `json:"Config"`
	HostConfig struct {
		Memory        int64 `json:"Memory"`
		NanoCPUs      int64 `json:"NanoCpus"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

// Image is an entry of GET /images/json.
type Image struct {
	ID          string            `json:"Id"`
	ParentID    string            `json:"ParentId"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	SharedSize  int64             `json:"SharedSize"`
	Containers  int64             `json:"Containers"` // -1 unless computed (see DiskUsage)
	Labels      map[string]string `json:"Labels"`
}

type Volume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Scope      string            `json:"Scope"`
	UsageData  *struct {
		Size     int64 `json:"Size"`     // -1 if not available
		RefCount int64 `json:"RefCount"` // -1 if not available
	} `json:"UsageData,omitempty"`
}

type IPAMConfig struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

type Network struct {
	Name     string `json:"Name"`
	ID       string `json:"Id"`
	Created  string `json:"Created"`
	Scope    string `json:"Scope"`
	Driver   string `json:"Driver"`
	Internal bool   `json:"Internal"`
	IPAM     struct {
		Config []IPAMConfig `json:"Config"`
	} `json:"IPAM"`
	Labels map[string]string `json:"Labels"`
}

// DiskUsage is GET /system/df.
type DiskUsage struct {
	LayersSize int64       `json:"LayersSize"`
	Images     []Image     `json:"Images"`
	Containers []Container `json:"Containers"`
	Volumes    []Volume    `json:"Volumes"`
}

type PruneReport struct {
	ContainersDeleted []string `json:"ContainersDeleted,omitempty"`
	ImagesDeleted     []any    `json:"ImagesDeleted,omitempty"`
	VolumesDeleted    []string `json:"VolumesDeleted,omitempty"`
	NetworksDeleted   []string `json:"NetworksDeleted,omitempty"`
	SpaceReclaimed    int64    `json:"SpaceReclaimed"`
}

// StatsJSON is one sample from GET /containers/{id}/stats.
type StatsJSON struct {
	Read     string `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// MemoryUsed is usage minus page cache, matching `docker stats`.
func (s StatsJSON) MemoryUsed() uint64 {
	u := s.MemoryStats.Usage
	cache := s.MemoryStats.Stats["inactive_file"]                // cgroup v2
	if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok { // cgroup v1
		cache = v
	}
	if cache < u {
		return u - cache
	}
	return u
}
