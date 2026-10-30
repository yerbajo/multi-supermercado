// Package web holds the aggregator's front end as embedded files, so the server runs
// from any directory and the page is self-contained: no CDN, no web font, no external
// URL of any kind. The browser downloads three small files and nothing else.
package web

import "embed"

// Files is index.html, styles.css, app.js and the favicon, served by cmd/web.
//
//go:embed index.html styles.css app.js favicon.svg
var Files embed.FS
