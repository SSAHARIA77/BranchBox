// Package gitsrc fetches the onboarded repository and exports the source of
// a single service at a given branch — without a working-tree checkout.
package gitsrc

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Repo is a local bare mirror of a remote repository.
type Repo struct {
	URL string
	Dir string
	mu  sync.Mutex
}

// Fetch clones the mirror on first use and updates every branch afterwards.
func (r *Repo) Fetch(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := os.Stat(filepath.Join(r.Dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(r.Dir), 0o755); err != nil {
			return err
		}
		_, err := git(ctx, "", "clone", "--mirror", r.URL, r.Dir)
		return err
	}
	// The configured URL may have changed since the mirror was created
	// (e.g. local folder -> GitHub); always fetch from the current one.
	if _, err := git(ctx, r.Dir, "remote", "set-url", "origin", r.URL); err != nil {
		return err
	}
	_, err := git(ctx, r.Dir, "fetch", "--prune", "origin")
	return err
}

// Resolve returns the commit SHA a branch (or tag / SHA) points to.
func (r *Repo) Resolve(ctx context.Context, ref string) (string, error) {
	for _, candidate := range []string{"refs/heads/" + ref, ref} {
		out, err := git(ctx, r.Dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}")
		if err == nil {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("branch %q not found in %s", ref, r.URL)
}

// Export writes the subtree subdir of commit into dest.
func (r *Repo) Export(ctx context.Context, commit, subdir, dest string) error {
	treeish := commit
	if subdir = strings.Trim(filepath.ToSlash(subdir), "/"); subdir != "" && subdir != "." {
		treeish += ":" + subdir
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.Dir, "archive", "--format=tar", treeish)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := untar(out, dest)
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %v: %s", treeish, err, strings.TrimSpace(stderr.String()))
	}
	return extractErr
}

func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
