package dockerx

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

// Container holds the running-container metadata needed by the agent.
type Container struct {
	ID    string
	Name  string
	Image string
	// RunningImageID is the image this container actually runs, independent of
	// where the shared :auto tag points (several validators may share one host).
	RunningImageID string
}

// Client wraps the Docker SDK client and exposes the operations the agent needs.
type Client struct {
	cli *dockerclient.Client
}

// New creates a Client from the environment (DOCKER_HOST, TLS vars, etc.).
func New() (*Client, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Discover returns running containers whose short image name is in managed.
func (c *Client) Discover(managed map[string]bool) ([]Container, error) {
	ctx := context.Background()
	list, err := c.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}
	var res []Container
	for _, s := range list {
		// Use Config.Image, not the list summary's Image: once :auto moves, the
		// list API reports a raw SHA and short-name matching breaks.
		insp, err := c.cli.ContainerInspect(ctx, s.ID)
		if err != nil {
			continue
		}
		img := insp.Config.Image
		if !managed[ShortImageName(img)] {
			continue
		}
		res = append(res, Container{
			ID:             s.ID,
			Name:           strings.TrimPrefix(insp.Name, "/"),
			Image:          img,
			RunningImageID: insp.Image,
		})
	}
	return res, nil
}

// FindContainerEnv returns env var key from the first running container that defines it.
func (c *Client) FindContainerEnv(key string) (string, bool) {
	ctx := context.Background()
	list, err := c.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return "", false
	}
	prefix := key + "="
	for _, s := range list {
		insp, err := c.cli.ContainerInspect(ctx, s.ID)
		if err != nil {
			continue
		}
		for _, e := range insp.Config.Env {
			if v, ok := strings.CutPrefix(e, prefix); ok {
				return v, true
			}
		}
	}
	return "", false
}

// ImageExists reports whether ref is present in the local image store.
func (c *Client) ImageExists(ref string) bool {
	ctx := context.Background()
	_, _, err := c.cli.ImageInspectWithRaw(ctx, ref)
	return err == nil
}

// ImageID returns the content-addressable ID for ref, or "" if not found.
func (c *Client) ImageID(ref string) string {
	ctx := context.Background()
	info, _, err := c.cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return ""
	}
	return info.ID
}

// EnsureAutoTag creates <repo>:auto pointing at the running image if it is missing.
func (c *Client) EnsureAutoTag(cont Container) (string, error) {
	ctx := context.Background()
	autoRef := RepoNoTag(cont.Image) + ":auto"
	if c.ImageExists(autoRef) {
		return autoRef, nil
	}
	if err := c.cli.ImageTag(ctx, cont.Image, autoRef); err != nil {
		return "", fmt.Errorf("tag %s → %s: %w", cont.Image, autoRef, err)
	}
	return autoRef, nil
}

// pullTimeout stops a stalled pull from hanging the cycle; completed layers stay
// cached, so the next cycle resumes cheaply.
const pullTimeout = 15 * time.Minute

// PullAndRetag pulls repo:ver, optionally verifies the digest, then retags it as repo:auto.
func (c *Client) PullAndRetag(repo, ver, digest string) error {
	ctx := context.Background()
	targetRef := repo + ":" + ver

	// Inherit the platform of the current :auto image so amd64-only images pull on arm64 hosts.
	var pullOpts image.PullOptions
	if info, _, err := c.cli.ImageInspectWithRaw(ctx, repo+":auto"); err == nil {
		if info.Os != "" && info.Architecture != "" {
			pullOpts.Platform = info.Os + "/" + info.Architecture
		}
	}

	pullCtx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	rc, err := c.cli.ImagePull(pullCtx, targetRef, pullOpts)
	if err != nil {
		return fmt.Errorf("pull %s: %w", targetRef, err)
	}
	streamErr := jsonmessage.DisplayJSONMessagesStream(rc, io.Discard, 0, false, nil)
	rc.Close()
	if streamErr != nil {
		return fmt.Errorf("pull %s: %w", targetRef, streamErr)
	}

	if digest != "" {
		info, _, err := c.cli.ImageInspectWithRaw(ctx, targetRef)
		if err != nil {
			return fmt.Errorf("inspect %s after pull: %w", targetRef, err)
		}
		found := false
		for _, d := range info.RepoDigests {
			if strings.Contains(d, digest) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("digest mismatch: manifest=%s image=%v", digest, info.RepoDigests)
		}
	}

	autoRef := repo + ":auto"
	if err := c.cli.ImageTag(ctx, targetRef, autoRef); err != nil {
		return fmt.Errorf("tag %s → %s: %w", targetRef, autoRef, err)
	}
	return nil
}

// WaitHealthy blocks until the container reports healthy or timeout elapses.
// Containers without a HEALTHCHECK return nil immediately.
func (c *Client) WaitHealthy(containerID string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := c.cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect while waiting for health: %w", err)
		}
		if info.State.Health == nil {
			return nil
		}
		if info.State.Health.Status == "healthy" {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	id := containerID
	if len(id) > 12 {
		id = id[:12]
	}
	return fmt.Errorf("container %s did not become healthy within %s", id, timeout)
}

// Recreate replaces the container with one using the same name, Config,
// HostConfig and networks, but with Config.Image set to image. Returns the new ID.
func (c *Client) Recreate(cont Container, image string) (string, error) {
	ctx := context.Background()

	info, err := c.cli.ContainerInspect(ctx, cont.ID)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", cont.Name, err)
	}
	info.Config.Image = image
	name := strings.TrimPrefix(info.Name, "/")

	stopTimeout := 30 * time.Second
	if err := c.cli.ContainerStop(ctx, cont.ID, container.StopOptions{
		Timeout: timeoutSeconds(stopTimeout),
	}); err != nil {
		return "", fmt.Errorf("stop %s: %w", name, err)
	}
	if err := c.cli.ContainerRemove(ctx, cont.ID, container.RemoveOptions{}); err != nil {
		return "", fmt.Errorf("remove %s: %w", name, err)
	}

	// Some API versions reject ContainerCreate with more than one endpoint, so
	// create with one network and NetworkConnect the rest afterwards.
	var firstNet string
	var firstEp *network.EndpointSettings
	extraNets := map[string]*network.EndpointSettings{}
	if info.NetworkSettings != nil {
		for netName, ep := range info.NetworkSettings.Networks {
			if firstEp == nil {
				firstNet = netName
				firstEp = ep
			} else {
				extraNets[netName] = ep
			}
		}
	}
	netCfg := &network.NetworkingConfig{}
	if firstEp != nil {
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			firstNet: firstEp,
		}
	}

	resp, err := c.cli.ContainerCreate(ctx, info.Config, info.HostConfig, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}

	for netName, ep := range extraNets {
		if err := c.cli.NetworkConnect(ctx, netName, resp.ID, ep); err != nil {
			return "", fmt.Errorf("connect %s to network %s: %w", name, netName, err)
		}
	}

	if err := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start %s: %w", name, err)
	}
	return resp.ID, nil
}

// timeoutSeconds returns nil (daemon default) for non-positive durations.
func timeoutSeconds(d time.Duration) *int {
	s := int(d.Seconds())
	if s <= 0 {
		return nil
	}
	return &s
}

// ShortImageName extracts the bare image name without registry, path, or tag.
// e.g. ghcr.io/foo/validator-app:auto -> validator-app
func ShortImageName(ref string) string {
	r := RepoNoTag(ref)
	if i := strings.LastIndex(r, "/"); i >= 0 {
		return r[i+1:]
	}
	return r
}

// RepoNoTag strips the tag, treating the part after the last ':' as a tag only
// when it contains no '/' (so a registry:port host is not mistaken for a tag).
func RepoNoTag(ref string) string {
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		return ref[:i]
	}
	return ref
}
