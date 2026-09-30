// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// hack/e2e/ocipush pushes an image from a `docker save` archive (OCI layout:
// index.json and blobs/sha256) to a registry over the distribution API, under
// one tag. The manifest is pushed byte for byte, so the registry serves the
// digest the image has locally. hack/e2e/components/registry.sh seeds the
// live e2e registry with it; docker push can't, because the registry is plain
// HTTP on a NodePort.
//
//	ocipush -archive podinfo.tar -registry http://10.0.0.2:30500 -repo e2e/podinfo -tag 6.13.0
//
// Only the standard library, so it builds without downloading modules.
package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	ociIndex    = "application/vnd.oci.image.index.v1+json"
	dockerList  = "application/vnd.docker.distribution.manifest.list.v2+json"
	manifestAcc = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
)

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform,omitempty"`
}

type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"`
}

var httpc = &http.Client{Timeout: 5 * time.Minute}

func main() {
	archive := flag.String("archive", "", "docker save archive")
	registry := flag.String("registry", "", "registry base URL, e.g. http://host:5000")
	repo := flag.String("repo", "", "repository, e.g. e2e/podinfo")
	tag := flag.String("tag", "", "tag to push")
	flag.Parse()
	if *archive == "" || *registry == "" || *repo == "" || *tag == "" {
		log.Fatal("usage: ocipush -archive FILE -registry URL -repo NAME -tag TAG")
	}
	files, err := readArchive(*archive)
	if err != nil {
		log.Fatal(err)
	}
	mediaType, digest, raw, err := imageManifest(files)
	if err != nil {
		log.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Fatalf("manifest %s: %v", digest, err)
	}
	base := strings.TrimSuffix(*registry, "/") + "/v2/" + *repo
	for _, d := range append([]descriptor{m.Config}, m.Layers...) {
		blob, ok := files[blobPath(d.Digest)]
		if !ok {
			log.Fatalf("blob %s is not in %s", d.Digest, *archive)
		}
		if err := pushBlob(base, d.Digest, blob); err != nil {
			log.Fatal(err)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/manifests/"+*tag, bytes.NewReader(raw))
	req.Header.Set("Content-Type", mediaType)
	if err := expect(req, http.StatusCreated); err != nil {
		log.Fatalf("push manifest: %v", err)
	}
	fmt.Printf("%s:%s %s\n", *repo, *tag, digest)
}

// readArchive returns every regular file of the tar by name.
func readArchive(path string) (map[string][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	files := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[strings.TrimPrefix(h.Name, "./")] = b
	}
}

func blobPath(digest string) string { return "blobs/" + strings.Replace(digest, ":", "/", 1) }

// imageManifest follows index.json to the linux/amd64 image manifest.
func imageManifest(files map[string][]byte) (string, string, []byte, error) {
	raw, ok := files["index.json"]
	if !ok {
		return "", "", nil, fmt.Errorf("no index.json: not an OCI layout archive (docker save of Docker 25+)")
	}
	mediaType, digest := ociIndex, ""
	for i := 0; i < 3; i++ {
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", "", nil, err
		}
		if mediaType != ociIndex && mediaType != dockerList {
			return mediaType, digest, raw, nil
		}
		var pick *descriptor
		for j := range m.Manifests {
			d := &m.Manifests[j]
			if d.Platform == nil || (d.Platform.OS == "linux" && d.Platform.Architecture == "amd64") {
				pick = d
				break
			}
		}
		if pick == nil {
			return "", "", nil, fmt.Errorf("no linux/amd64 manifest in %s", digest)
		}
		mediaType, digest = pick.MediaType, pick.Digest
		if raw, ok = files[blobPath(digest)]; !ok {
			return "", "", nil, fmt.Errorf("manifest %s is not in the archive", digest)
		}
	}
	return "", "", nil, fmt.Errorf("index nesting too deep")
}

// pushBlob uploads a blob the repository does not have yet (monolithic upload).
func pushBlob(base, digest string, blob []byte) error {
	if resp, err := httpc.Head(base + "/blobs/" + digest); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}
	resp, err := httpc.Post(base+"/blobs/uploads/", "application/octet-stream", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("start upload of %s: HTTP %d", digest, resp.StatusCode)
	}
	loc, err := url.Parse(base)
	if err != nil {
		return err
	}
	if loc, err = loc.Parse(resp.Header.Get("Location")); err != nil {
		return err
	}
	q := loc.Query()
	q.Set("digest", digest)
	loc.RawQuery = q.Encode()
	req, _ := http.NewRequest(http.MethodPut, loc.String(), bytes.NewReader(blob))
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := expect(req, http.StatusCreated); err != nil {
		return fmt.Errorf("upload %s: %v", digest, err)
	}
	return nil
}

func expect(req *http.Request, status int) error {
	req.Header.Set("Accept", manifestAcc)
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}
