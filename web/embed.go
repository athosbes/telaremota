// Package web embute o visualizador (HTML/JS/CSS) no binário do servidor.
package web

import "embed"

//go:embed static
var Static embed.FS
