package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Docker runs sandboxes as containers on the baseline's Docker network,
// driving the docker CLI (so every action is an ordinary, inspectable
// docker command).
//
// Each sandbox is a pair of containers, mirroring a Kubernetes pod:
//
//	bb-<id>-<service>       the sidecar; owns the network namespace, has the
//	                        sandbox's DNS name and installs iptables first
//	bb-<id>-<service>-app   the service built from the branch, joined to the
//	                        sidecar's namespace so its outbound calls are
//	                        intercepted from the very first connection
type Docker struct {
	ComposeProject string // compose project of the baseline
	Network        string // network the baseline runs on
	ProxyImage     string // branchbox proxy image (sidecar mode)
	ControllerURL  string // controller address as seen from the network
	Token          string // API token handed to sandbox sidecars
}

func (d *Docker) podName(s SandboxSpec) string { return "bb-" + s.ID + "-" + s.Service }
func (d *Docker) appName(s SandboxSpec) string { return d.podName(s) + "-app" }

// Build implements Runtime.
func (d *Docker) Build(ctx context.Context, spec BuildSpec, log io.Writer) error {
	args := []string{"build", "--progress=plain", "-t", spec.Tag,
		"-f", filepath.Join(spec.ContextDir, spec.Dockerfile)}
	for k, v := range spec.Labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, spec.ContextDir)
	fmt.Fprintf(log, "$ docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed (see build log): %w", err)
	}
	return nil
}

// Deploy implements Runtime.
func (d *Docker) Deploy(ctx context.Context, s SandboxSpec) (string, error) {
	env, err := d.baselineEnv(ctx, s.Service)
	if err != nil {
		return "", err
	}
	_ = d.Destroy(ctx, s) // clear leftovers from an earlier attempt
	labels := []string{
		"--label", "branchbox.managed=true",
		"--label", "branchbox.sandbox=" + s.ID,
		"--label", "branchbox.service=" + s.Service,
	}

	pod := d.podName(s)
	sidecar := append([]string{"run", "-d", "--name", pod, "--network", d.Network,
		"--cap-add", "NET_ADMIN", "--label", "branchbox.role=sidecar"}, labels...)
	sidecar = append(sidecar,
		"-e", "BB_SERVICE="+s.Service,
		"-e", "BB_SANDBOX="+s.ID,
		"-e", "BB_CONTROLLER_URL="+d.ControllerURL,
		"-e", "BB_API_TOKEN="+d.Token,
		d.ProxyImage, "sidecar")
	if _, err := docker(ctx, sidecar...); err != nil {
		return "", err
	}
	if err := waitHTTP(ctx, "http://"+pod+":15000/ready", 30*time.Second); err != nil {
		return "", fmt.Errorf("sidecar did not become ready: %w", err)
	}

	app := append([]string{"run", "-d", "--name", d.appName(s), "--network", "container:" + pod,
		"--label", "branchbox.role=app"}, labels...)
	for _, kv := range env {
		app = append(app, "-e", kv)
	}
	app = append(app, s.Image)
	if _, err := docker(ctx, app...); err != nil {
		return "", err
	}

	addr := net.JoinHostPort(pod, strconv.Itoa(s.Port))
	if err := d.waitListening(ctx, d.appName(s), addr, 3*time.Minute); err != nil {
		return "", err
	}
	return addr, nil
}

// Destroy implements Runtime.
func (d *Docker) Destroy(ctx context.Context, s SandboxSpec) error {
	_, err := docker(ctx, "rm", "-f", d.appName(s), d.podName(s))
	if err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}
	return nil
}

// RemoveImage deletes a sandbox image; failures are not fatal.
func (d *Docker) RemoveImage(ctx context.Context, image string) {
	_, _ = docker(ctx, "rmi", image)
}

// Alive implements Runtime.
func (d *Docker) Alive(ctx context.Context, s SandboxSpec) bool {
	out, err := docker(ctx, "inspect", "-f", "{{.State.Running}}", d.podName(s), d.appName(s))
	return err == nil && strings.Count(out, "true") == 2
}

// Logs implements Runtime.
func (d *Docker) Logs(ctx context.Context, s SandboxSpec, c Component, tail int, follow bool, w io.Writer) error {
	name := d.appName(s)
	if c == ComponentSidecar {
		name = d.podName(s)
	}
	args := []string{"logs", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, name)...)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// baselineEnv returns the environment the baseline container of service
// was started with, minus the variables baked into its image. The sandbox
// therefore gets exactly the same configuration (downstream addresses,
// ports, feature flags) as the service it replaces, with no duplication in
// BranchBox's own config.
func (d *Docker) baselineEnv(ctx context.Context, service string) ([]string, error) {
	id, err := d.findBaseline(ctx, service)
	if err != nil {
		return nil, err
	}
	return d.composeEnv(ctx, id)
}

// findBaseline returns the ID of the running production container of
// service, identified by its compose labels.
func (d *Docker) findBaseline(ctx context.Context, service string) (string, error) {
	id, err := docker(ctx, "ps", "-q",
		"--filter", "label=com.docker.compose.project="+d.ComposeProject,
		"--filter", "label=com.docker.compose.service="+service)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(strings.SplitN(id, "\n", 2)[0])
	if id == "" {
		return "", fmt.Errorf("baseline container for %q is not running (compose project %q)", service, d.ComposeProject)
	}
	return id, nil
}

// composeEnv returns the environment a container was started with, minus
// the variables baked into its image.
func (d *Docker) composeEnv(ctx context.Context, id string) ([]string, error) {
	var containerEnv, imageEnv []string
	out, err := docker(ctx, "inspect", "-f", "{{json .Config.Env}}", id)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(out), &containerEnv); err != nil {
		return nil, err
	}
	image, err := docker(ctx, "inspect", "-f", "{{.Config.Image}}", id)
	if err != nil {
		return nil, err
	}
	out, err = docker(ctx, "image", "inspect", "-f", "{{json .Config.Env}}", strings.TrimSpace(image))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(out), &imageEnv); err != nil {
		return nil, err
	}
	fromImage := map[string]bool{}
	for _, kv := range imageEnv {
		fromImage[kv] = true
	}
	var env []string
	for _, kv := range containerEnv {
		if !fromImage[kv] {
			env = append(env, kv)
		}
	}
	return env, nil
}

// BaselineOf implements Runtime.
func (d *Docker) BaselineOf(ctx context.Context, service string) (Baseline, error) {
	id, err := d.findBaseline(ctx, service)
	if err != nil {
		return Baseline{}, err
	}
	out, err := docker(ctx, "inspect", "-f", "{{.Config.Image}}|{{index .Config.Labels \""+LabelUpstreamImage+"\"}}", id)
	if err != nil {
		return Baseline{}, err
	}
	image, upstream, _ := strings.Cut(strings.TrimSpace(out), "|")
	if upstream == "" || upstream == "<no value>" {
		upstream = image
	}
	return Baseline{Container: id, Image: image, UpstreamImage: upstream}, nil
}

// ReplaceBaseline implements Runtime. The production container is removed
// and recreated from image with the same name, environment, labels,
// restart policy and network namespace (its sidecar's), so its address and
// interception are unchanged. If the new version does not start listening,
// the previous container configuration is restored and an error returned.
func (d *Docker) ReplaceBaseline(ctx context.Context, service string, port int, image string, labels map[string]string) error {
	id, err := d.findBaseline(ctx, service)
	if err != nil {
		return err
	}
	out, err := docker(ctx, "inspect", id)
	if err != nil {
		return err
	}
	var meta []struct {
		Name   string
		Config struct {
			Image  string
			Labels map[string]string
		}
		HostConfig struct {
			NetworkMode   string
			RestartPolicy struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(out), &meta); err != nil || len(meta) != 1 {
		return fmt.Errorf("inspecting %s: %v", service, err)
	}
	env, err := d.composeEnv(ctx, id)
	if err != nil {
		return err
	}
	m := meta[0]
	name := strings.TrimPrefix(m.Name, "/")
	runArgs := func(image string, labels map[string]string) []string {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		args := []string{"run", "-d", "--name", name, "--network", m.HostConfig.NetworkMode}
		if p := m.HostConfig.RestartPolicy.Name; p != "" && p != "no" {
			args = append(args, "--restart", p)
		}
		for _, k := range keys {
			args = append(args, "--label", k+"="+labels[k])
		}
		for _, kv := range env {
			args = append(args, "-e", kv)
		}
		return append(args, image)
	}
	newLabels := maps.Clone(m.Config.Labels)
	if newLabels == nil {
		newLabels = map[string]string{}
	}
	maps.Copy(newLabels, labels)
	addr := net.JoinHostPort(service, strconv.Itoa(port))

	if _, err := docker(ctx, "rm", "-f", id); err != nil {
		return err
	}
	_, err = docker(ctx, runArgs(image, newLabels)...)
	if err == nil {
		err = d.waitListening(ctx, name, addr, 3*time.Minute)
	}
	if err == nil {
		return nil
	}

	// Roll back to exactly what was running before.
	rollback := context.Background()
	_, _ = docker(rollback, "rm", "-f", name)
	if _, rbErr := docker(rollback, runArgs(m.Config.Image, m.Config.Labels)...); rbErr != nil {
		return fmt.Errorf("%v; ROLLBACK FAILED: %v", err, rbErr)
	}
	if rbErr := d.waitListening(rollback, name, addr, 3*time.Minute); rbErr != nil {
		return fmt.Errorf("%v; rolled back, but the previous version is not listening: %v", err, rbErr)
	}
	return fmt.Errorf("%v (rolled back to %s)", err, m.Config.Image)
}

// ImageExists implements Runtime.
func (d *Docker) ImageExists(ctx context.Context, image string) bool {
	_, err := docker(ctx, "image", "inspect", "-f", "{{.Id}}", image)
	return err == nil
}

// waitListening waits until a service accepts TCP connections at addr,
// failing fast if its container exits.
func (d *Docker) waitListening(ctx context.Context, container, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return nil
		}
		if out, err := docker(ctx, "inspect", "-f", "{{.State.Running}}", container); err == nil && strings.TrimSpace(out) == "false" {
			logs, _ := docker(ctx, "logs", "--tail", "20", container)
			return fmt.Errorf("service container exited during startup:\n%s", logs)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("service did not start listening on %s within %s", addr, timeout)
}

func waitHTTP(ctx context.Context, url string, timeout time.Duration) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %s", resp.Status)
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return lastErr
}

func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
