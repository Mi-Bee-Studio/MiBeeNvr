package onvif

import (
	"context"
	"fmt"
	"sync"

	onvifgo "github.com/0x524a/onvif-go"
)

// SnapshotProviderImpl implements SnapshotProvider by delegating to onvif-go's media service.
type SnapshotProviderImpl struct {
	client       *onvifgo.Client
	profileToken string
	mu           sync.Mutex
}

// NewSnapshotProvider creates a SnapshotProvider backed by an onvif-go client.
func NewSnapshotProvider(client *onvifgo.Client, profileToken string) *SnapshotProviderImpl {
	return &SnapshotProviderImpl{
		client:       client,
		profileToken: profileToken,
	}
}

// GetSnapshotUri returns the snapshot URI for the camera.
func (s *SnapshotProviderImpl) GetSnapshotUri(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mediaURI, err := s.client.GetSnapshotURI(ctx, s.profileToken)
	if err != nil {
		return "", fmt.Errorf("get snapshot URI failed: %w", err)
	}
	if mediaURI == nil {
		return "", fmt.Errorf("get snapshot URI returned nil")
	}
	return mediaURI.URI, nil
}
