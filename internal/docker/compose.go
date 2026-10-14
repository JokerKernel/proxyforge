package docker

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"text/template"
)

//go:embed templates/compose.yaml.tmpl
var composeTemplate string
var shaPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/xtls/xray-core@sha256:[0-9a-f]{64}$`)

func validImageRef(s string) bool { return imagePattern.MatchString(s) }
func validImageID(s string) bool  { return shaPattern.MatchString(s) }
func renderCompose(image string) ([]byte, error) {
	if !validImageRef(image) {
		return nil, fmt.Errorf("镜像必须是官方仓库的固定 digest")
	}
	tmpl, err := template.New("compose").Parse(composeTemplate)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	err = tmpl.Execute(&b, image)
	return b.Bytes(), err
}
