//go:build embed_pir

package pir

import _ "embed"

//go:generate sh -c "test -f bin/nf-server || (echo 'ERROR: bin/nf-server not found — run make pir-binary first' && exit 1)"

//go:embed bin/nf-server
var nfServerBinary []byte
