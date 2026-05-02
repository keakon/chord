module github.com/keakon/chord

go 1.26

require (
	charm.land/bubbles/v2 v2.0.0
	charm.land/bubbletea/v2 v2.0.1
	charm.land/glamour/v2 v2.0.0
	charm.land/lipgloss/v2 v2.0.0
	github.com/alecthomas/chroma/v2 v2.16.0
	github.com/atotto/clipboard v0.1.4
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/bytedance/sonic v1.15.1-0.20260305062320-c9e5b0f6896d
	github.com/charmbracelet/ultraviolet v0.0.0-20260303162955-0b88c25f3fff
	github.com/charmbracelet/x/ansi v0.11.6
	github.com/charmbracelet/x/powernap v0.1.3
	github.com/charmbracelet/x/term v0.2.2
	github.com/dgraph-io/ristretto/v2 v2.4.0
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/keakon/golog v0.0.0-20260502025539-27a204d70aac
	github.com/mackee/go-readability v0.3.1
	github.com/mattn/go-runewidth v0.0.20
	github.com/muesli/cancelreader v0.2.2
	github.com/muesli/reflow v0.3.0
	github.com/rivo/uniseg v0.4.7
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	github.com/wlynxg/chardet v1.0.4
	golang.org/x/net v0.48.0
	golang.org/x/sys v0.42.0
	golang.org/x/text v0.32.0
	golang.org/x/time v0.14.0
	gopkg.in/yaml.v3 v3.0.1
	mvdan.cc/sh/v3 v3.13.1
)

// TEMP: keep this replace until upstream accepts our required powernap fixes.
// Pin to an immutable pseudo-version for reproducible builds.
replace github.com/charmbracelet/x/powernap => github.com/keakon/x/powernap v0.0.0-20260330063338-2b2cb686f9cc

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.2 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/sourcegraph/jsonrpc2 v0.2.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yuin/goldmark v1.7.8 // indirect
	github.com/yuin/goldmark-emoji v1.0.5 // indirect
	golang.org/x/arch v0.11.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
)
