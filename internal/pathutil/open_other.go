//go:build !unix

package pathutil

import "os"

const canonicalReadFlags = os.O_RDONLY
