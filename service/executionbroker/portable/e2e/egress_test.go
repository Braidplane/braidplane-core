//go:build portable_executor

package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	containerlog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dindImage  = "docker:29.8.2-dind@sha256:1e08cdb63405ca788aea94ef35b792d1e299607c667d33d7bcbcae1fd2611ced"
	probeImage = "alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a"
)

func TestPortableExecutorHostEgressPolicy(t *testing.T) {
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{}}`)
	containerlog.SetDefault(containerlog.NewNoopLogger())
	require.False(t, testcontainers.ReadConfig().Config.RyukDisabled, "resource reaper must remain enabled")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	// Only this disposable inner Docker host is privileged. It receives no host socket or host filesystem mount.
	host := startContainer(t, ctx, testcontainers.ContainerRequest{
		Image: dindImage, Env: map[string]string{"DOCKER_TLS_CERTDIR": "", "DOCKER_IPTABLES_LEGACY": ""},
		Entrypoint: []string{"sh", "-ec"}, Cmd: []string{"apk add --no-cache bash >/dev/null; exec dockerd-entrypoint.sh dockerd --host=unix:///var/run/docker.sock --storage-driver=vfs --ipv6 --fixed-cidr-v6=fd00:1328::/64"},
		Files: []testcontainers.ContainerFile{{HostFilePath: filepath.Join(root, "deploy/staging-worker/install-egress-policy.sh"), ContainerFilePath: "/fixture/install-egress-policy.sh", FileMode: 0o444}},
		HostConfigModifier: func(h *container.HostConfig) {
			h.Privileged = true
			h.Memory = 1024 * 1024 * 1024
			h.MemorySwap = h.Memory
			h.NanoCPUs = 1000000000
			pids := int64(512)
			h.PidsLimit = &pids
			h.Tmpfs = map[string]string{"/var/lib/docker": "rw,exec,nosuid,nodev,size=512m"}
			h.LogConfig = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}}
		}, WaitingFor: wait.ForExec([]string{"docker", "info"}).WithStartupTimeout(2 * time.Minute),
	})
	id := host.GetContainerID()
	docker(t, ctx, "exec", id, "sh", "-ec", `if nc -z -w 1 127.0.0.1 2375; then exit 1; fi; if nc -z -w 1 127.0.0.1 2376; then exit 1; fi`)
	docker(t, ctx, "exec", id, "docker", "pull", probeImage)
	// The canary subnet must be routable so Docker's earlier unpublished-port guard cannot mask this policy.
	docker(t, ctx, "exec", id, "docker", "network", "create", "--subnet=10.72.0.0/24", "--opt", "com.docker.network.bridge.gateway_mode_ipv4=nat-unprotected", "app")
	docker(t, ctx, "exec", id, "docker", "network", "create", "--subnet=172.31.72.0/24", "worker")
	docker(t, ctx, "exec", id, "docker", "run", "-d", "--name", "database", "--network", "app", "--ip", "10.72.0.2", probeImage, "/bin/sh", "-c", "while true; do nc -l -p 5432 >/dev/null; done")
	docker(t, ctx, "exec", id, "docker", "run", "-d", "--name", "denied", "--network", "app", "--ip", "10.72.0.3", probeImage, "/bin/sh", "-c", "while true; do nc -l -p 8080 >/dev/null; done")
	docker(t, ctx, "exec", id, "docker", "run", "--rm", "--network", "app", probeImage, "sh", "-ec", "nc -z -w 2 10.72.0.2 5432; nc -z -w 2 10.72.0.3 8080")
	for range 2 {
		docker(t, ctx, "exec", id, "env", "BRAIDPLANE_WORKER_BRIDGE_CIDR=172.31.72.0/24", "BRAIDPLANE_APP_PRIVATE_IPV4=10.72.0.2", "bash", "/fixture/install-egress-policy.sh")
	}
	jumps := docker(t, ctx, "exec", id, "iptables", "-S", "DOCKER-USER")
	require.Equal(t, 1, strings.Count(jumps, "-s 172.31.72.0/24 -j BRAIDPLANE-WORKER-EGRESS"), "policy installation must be idempotent")
	docker(t, ctx, "exec", id, "docker", "run", "-d", "--name", "probe", "--network", "worker", "--ip", "172.31.72.2", probeImage, "sleep", "180")
	allowed := false
	for range 10 {
		if exec.CommandContext(ctx, "docker", "exec", id, "docker", "exec", "probe", "nc", "-z", "-w", "2", "10.72.0.2", "5432").Run() == nil {
			allowed = true
			break
		}
	}
	if !allowed {
		output, _ := exec.CommandContext(ctx, "docker", "exec", id, "docker", "exec", "probe", "nc", "-z", "-w", "2", "10.72.0.2", "5432").CombinedOutput()
		t.Logf("database probe diagnostic: %.1024s", output)
		t.Log(docker(t, ctx, "exec", id, "docker", "exec", "probe", "ip", "route"))
		t.Log(docker(t, ctx, "exec", id, "docker", "exec", "probe", "ip", "neigh"))
		t.Log(docker(t, ctx, "exec", id, "ip", "route"))
		t.Log(docker(t, ctx, "exec", id, "iptables", "-S", "DOCKER-USER"))
		t.Log(docker(t, ctx, "exec", id, "sysctl", "net.ipv4.ip_forward"))
		t.Log(docker(t, ctx, "exec", id, "iptables", "-nvxL", "BRAIDPLANE-WORKER-EGRESS"))
		t.Log(docker(t, ctx, "exec", id, "iptables", "-nvxL", "FORWARD"))
	}
	require.True(t, allowed, "the database port exception must permit a real connection")
	docker(t, ctx, "exec", id, "sh", "-ec", `if docker exec probe nc -z -w 2 10.72.0.3 8080; then exit 1; fi; if docker exec probe nc -z -w 2 169.254.169.254 80; then exit 1; fi`)
	// Counters distinguish policy rejection from an absent listener, missing route or unrelated Docker isolation rule.
	docker(t, ctx, "exec", id, "sh", "-ec", `iptables -nvxL BRAIDPLANE-WORKER-EGRESS | awk '$3=="REJECT" && $9=="10.0.0.0/8" && $1>0 {found=1} END {exit !found}'`)
	docker(t, ctx, "exec", id, "sh", "-ec", `iptables -nvxL BRAIDPLANE-WORKER-EGRESS | awk '$3=="REJECT" && $9=="169.254.0.0/16" && $1>0 {found=1} END {exit !found}'`)
}
