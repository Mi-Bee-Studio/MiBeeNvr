package objectstore

// get.go — ranged GetObject: the read path for the batch-2 playback proxy.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	aws "github.com/aws/aws-sdk-go-v2/aws"
)

// ErrRangeNotSatisfiable is returned when the store replies 416 to a ranged
// GET, or ignores the Range header and returns 200 (a full-body reply to a
// ranged request would silently misalign the proxy's byte stream — surface
// it as an error instead).
var ErrRangeNotSatisfiable = errors.New("range not satisfiable")

// GetRange implements Store.
func (s *S3Store) GetRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, ObjectInfo, error) {
	if start < 0 {
		return nil, ObjectInfo{}, fmt.Errorf("getrange %s: negative start", key)
	}
	if end >= 0 && end < start {
		return nil, ObjectInfo{}, fmt.Errorf("getrange %s: end before start", key)
	}
	var rangeHdr string
	if end < 0 {
		rangeHdr = fmt.Sprintf("bytes=%d-", start)
	} else {
		rangeHdr = fmt.Sprintf("bytes=%d-%d", start, end)
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Range:  aws.String(rangeHdr),
	})
	if err != nil {
		var respErr *awshttp.ResponseError
		if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
			return nil, ObjectInfo{}, fmt.Errorf("getrange %s: %w", key, ErrObjectNotFound)
		}
		if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusRequestedRangeNotSatisfiable {
			return nil, ObjectInfo{}, fmt.Errorf("getrange %s: %w", key, ErrRangeNotSatisfiable)
		}
		return nil, ObjectInfo{}, fmt.Errorf("getrange %s: %w", key, err)
	}

	// A ranged request must yield 206. A 200 means the store ignored the
	// Range (or the object is smaller than the window) — treating it as
	// success would serve misaligned bytes to the proxy.
	if out.ContentRange == nil {
		_ = out.Body.Close()
		return nil, ObjectInfo{}, fmt.Errorf("getrange %s: store ignored Range header (no Content-Range): %w", key, ErrRangeNotSatisfiable)
	}

	info := ObjectInfo{Key: key}
	info.Size = parseTotalFromContentRange(*out.ContentRange)
	if out.ETag != nil {
		info.ETag = *out.ETag
	}
	return out.Body, info, nil
}

// parseTotalFromContentRange extracts the total size from
// "bytes 4-6/11" → 11. Returns 0 when malformed or "*" (unknown length).
func parseTotalFromContentRange(cr string) int64 {
	sep := strings.LastIndexByte(cr, '/')
	if sep < 0 || sep == len(cr)-1 {
		return 0
	}
	total, err := strconv.ParseInt(cr[sep+1:], 10, 64)
	if err != nil || total < 0 {
		return 0
	}
	return total
}
