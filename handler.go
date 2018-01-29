// Copyright 2017 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// govanityurls serves Go vanity URLs.
package main

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v2"
)

type handler struct {
	host         string
	cacheControl string
	paths        pathConfigSet
}

type pathConfig struct {
	path    string
	repo    string
	display string
	vcs     string
}

func newHandler(config []byte) (*handler, error) {
	var parsed struct {
		Host     string `yaml:"host,omitempty"`
		CacheAge *int64 `yaml:"cache_max_age,omitempty"`
		Paths    map[string]struct {
			Repo    string `yaml:"repo,omitempty"`
			Display string `yaml:"display,omitempty"`
			VCS     string `yaml:"vcs,omitempty"`
		} `yaml:"paths,omitempty"`
	}
	if err := yaml.Unmarshal(config, &parsed); err != nil {
		return nil, err
	}
	h := &handler{host: parsed.Host}
	cacheAge := int64(86400) // 24 hours (in seconds)
	if parsed.CacheAge != nil {
		cacheAge = *parsed.CacheAge
		if cacheAge < 0 {
			return nil, errors.New("cache_max_age is negative")
		}
	}
	h.cacheControl = fmt.Sprintf("public, max-age=%d", cacheAge)
	for path, e := range parsed.Paths {
		if user, repo, ok := isGitHubRepo(e.Repo); ok {
			base := "https://github.com/" + user + "/" + repo
			if e.VCS != "" && e.VCS != "git" {
				return nil, fmt.Errorf("configuration for %v: detected GitHub repository, but VCS = %s", path, e.VCS)
			}
			display := e.Display
			if display == "" {
				display = fmt.Sprintf("%v %v/tree/master{/dir} %v/blob/master{/dir}/{file}#L{line}", base, base, base)
			}
			h.paths = append(h.paths, pathConfig{
				path:    strings.TrimSuffix(path, "/"),
				repo:    base + ".git",
				display: display,
				vcs:     "git",
			})
			continue
		}
		if user, repo, isGit, ok := isBitbucketRepo(e.Repo); ok {
			base := "https://bitbucket.org/" + user + "/" + repo
			switch {
			case e.VCS == "hg":
				if isGit {
					return nil, fmt.Errorf("configuration for %v: VCS is hg, but repo has .git suffix", path)
				}
				display := e.Display
				if display == "" {
					display = fmt.Sprintf("%v %v/src/default{/dir} %v/src/default{/dir}/{file}#{file}-{line}", base, base, base)
				}
				h.paths = append(h.paths, pathConfig{
					path:    strings.TrimSuffix(path, "/"),
					repo:    base,
					display: display,
					vcs:     "hg",
				})
			case e.VCS == "git" || (e.VCS == "" && isGit):
				display := e.Display
				if display == "" {
					display = fmt.Sprintf("%v %v/src/master{/dir} %v/src/master{/dir}/{file}#{file}-{line}", base, base, base)
				}
				h.paths = append(h.paths, pathConfig{
					path:    strings.TrimSuffix(path, "/"),
					repo:    base + ".git",
					display: display,
					vcs:     "git",
				})
			case e.VCS == "" && !isGit:
				return nil, fmt.Errorf("configuration for %v: must specify either 'vcs: git' or 'vcs: hg' for Bitbucket repository", path)
			default:
				return nil, fmt.Errorf("configuration for %v: detected Bitbucket repository, but VCS = %s", path, e.VCS)
			}
			continue
		}
		if e.VCS == "" {
			return nil, fmt.Errorf("configuration for %v: cannot infer VCS from %s", path, e.Repo)
		} else if e.VCS != "bzr" && e.VCS != "git" && e.VCS != "hg" && e.VCS != "svn" {
			return nil, fmt.Errorf("configuration for %v: unknown VCS %s", path, e.VCS)
		}
		h.paths = append(h.paths, pathConfig{
			path:    strings.TrimSuffix(path, "/"),
			repo:    e.Repo,
			display: e.Display,
			vcs:     e.VCS,
		})
	}
	sort.Sort(h.paths)
	return h, nil
}

func isGitHubRepo(url string) (user, repo string, ok bool) {
	const httpsPrefix = "https://github.com/"
	if !strings.HasPrefix(url, httpsPrefix) {
		return "", "", false
	}
	path := url[len(httpsPrefix):]
	i := strings.IndexByte(path, '/')
	if i == -1 {
		return "", "", false
	}
	user, repo = path[:i], path[i+1:]
	if strings.Contains(repo, "/") {
		return "", "", false
	}
	return user, strings.TrimSuffix(repo, ".git"), true
}

func isBitbucketRepo(url string) (user, repo string, isGit bool, ok bool) {
	const httpsPrefix = "https://bitbucket.org/"
	if !strings.HasPrefix(url, httpsPrefix) {
		return "", "", false, false
	}
	path := url[len(httpsPrefix):]
	i := strings.IndexByte(path, '/')
	if i == -1 {
		return "", "", false, false
	}
	user, repo = path[:i], path[i+1:]
	if strings.Contains(repo, "/") {
		return "", "", false, false
	}
	const gitSuffix = ".git"
	if strings.HasSuffix(repo, gitSuffix) {
		return user, repo[:len(repo)-len(gitSuffix)], true, true
	}
	return user, repo, false, true
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	current := r.URL.Path
	pc, subpath := h.paths.find(current)
	if pc == nil && current == "/" {
		h.serveIndex(w, r)
		return
	}
	if pc == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", h.cacheControl)
	if err := vanityTmpl.Execute(w, struct {
		Import  string
		Subpath string
		Repo    string
		Display string
		VCS     string
	}{
		Import:  h.Host(r) + pc.path,
		Subpath: subpath,
		Repo:    pc.repo,
		Display: pc.display,
		VCS:     pc.vcs,
	}); err != nil {
		http.Error(w, "cannot render the page", http.StatusInternalServerError)
	}
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	host := h.Host(r)
	handlers := make([]string, len(h.paths))
	for i, h := range h.paths {
		handlers[i] = host + h.path
	}
	if err := indexTmpl.Execute(w, struct {
		Host     string
		Handlers []string
	}{
		Host:     host,
		Handlers: handlers,
	}); err != nil {
		http.Error(w, "cannot render the page", http.StatusInternalServerError)
	}
}

func (h *handler) Host(r *http.Request) string {
	host := h.host
	if host == "" {
		host = defaultHost(r)
	}
	return host
}

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
<h1>{{.Host}}</h1>
<ul>
{{range .Handlers}}<li><a href="https://godoc.org/{{.}}">{{.}}</a></li>{{end}}
</ul>
</html>
`))

var vanityTmpl = template.Must(template.New("vanity").Parse(`<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8"/>
<meta name="go-import" content="{{.Import}} {{.VCS}} {{.Repo}}">
<meta name="go-source" content="{{.Import}} {{.Display}}">
<meta http-equiv="refresh" content="0; url=https://godoc.org/{{.Import}}/{{.Subpath}}">
</head>
<body>
Nothing to see here; <a href="https://godoc.org/{{.Import}}/{{.Subpath}}">see the package on godoc</a>.
</body>
</html>`))

type pathConfigSet []pathConfig

func (pset pathConfigSet) Len() int {
	return len(pset)
}

func (pset pathConfigSet) Less(i, j int) bool {
	return pset[i].path < pset[j].path
}

func (pset pathConfigSet) Swap(i, j int) {
	pset[i], pset[j] = pset[j], pset[i]
}

func (pset pathConfigSet) find(path string) (pc *pathConfig, subpath string) {
	i := sort.Search(len(pset), func(i int) bool {
		return pset[i].path >= path
	})
	if i < len(pset) && pset[i].path == path {
		return &pset[i], ""
	}
	if i > 0 && strings.HasPrefix(path, pset[i-1].path+"/") {
		return &pset[i-1], path[len(pset[i-1].path)+1:]
	}
	return nil, ""
}
