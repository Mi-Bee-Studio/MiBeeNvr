package manscdp

import (
	"fmt"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// CharsetDecode converts data to UTF-8. Valid UTF-8 input is returned
// unchanged; otherwise GB18030 and then GBK are tried in order — the charsets
// Chinese camera vendors (Hikvision, Dahua, ...) emit Catalog XML in. If every
// decoder fails, the original bytes are returned with an error.
func CharsetDecode(data []byte) ([]byte, error) {
	if utf8.Valid(data) {
		return data, nil
	}
	for _, enc := range []encoding.Encoding{simplifiedchinese.GB18030, simplifiedchinese.GBK} {
		out, err := enc.NewDecoder().Bytes(data)
		if err == nil && utf8.Valid(out) {
			return out, nil
		}
	}
	return data, fmt.Errorf("manscdp: cannot decode input as UTF-8, GB18030, or GBK")
}
