package main

import _ "embed"

//go:embed assets/index.html
var indexHTML string

//go:embed assets/app.js
var appJS string

//go:embed assets/app.css
var appCSS string

//go:embed assets/fonts/geist-sans.woff2
var geistSans []byte

//go:embed assets/fonts/geist-mono.woff2
var geistMono []byte

// Branding assets are cropped and resized from the supplied metallist-logo.png.
//go:embed assets/metallist-logo.png
var brandLogo []byte

//go:embed assets/favicon.png
var favicon []byte

//go:embed assets/apple-touch-icon.png
var appleTouchIcon []byte
