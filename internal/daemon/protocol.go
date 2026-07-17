package daemon

import "encoding/json"

const (
	SessionProtocol = 1
	ControlProtocol = 1
	maxControlLine  = 64 * 1024
)

type HandshakeEnvelope struct {
	Handshake Handshake `json:"mcpmu_handshake"`
}

type Handshake struct {
	Type               string `json:"type"`
	Protocol           int    `json:"protocol,omitempty"`
	ControlProtocol    int    `json:"controlProtocol,omitempty"`
	Build              string `json:"build,omitempty"`
	ConfigPath         string `json:"configPath"`
	Namespace          string `json:"namespace,omitempty"`
	ExposeManagerTools bool   `json:"exposeManagerTools,omitempty"`
	Resources          bool   `json:"resources,omitempty"`
	Prompts            bool   `json:"prompts,omitempty"`
	Eager              bool   `json:"eager,omitempty"`
	PID                int    `json:"pid,omitempty"`
}

type HandshakeResponse struct {
	OK               bool   `json:"ok,omitempty"`
	Error            string `json:"error,omitempty"`
	DaemonBuild      string `json:"daemonBuild,omitempty"`
	DaemonConfigPath string `json:"daemonConfigPath,omitempty"`
}

type ControlRequest struct {
	Command string `json:"command"`
}

type ControlResponse struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Status *StatusResponse `json:"status,omitempty"`
}

type StatusResponse struct {
	Socket           string   `json:"socket"`
	Build            string   `json:"build"`
	Version          string   `json:"version"`
	Revision         string   `json:"revision"`
	ConfigPath       string   `json:"configPath"`
	PID              int      `json:"pid"`
	Sessions         int      `json:"sessions"`
	RunningUpstreams []string `json:"runningUpstreams"`
	Stopping         bool     `json:"stopping"`
}

func marshalLine(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
