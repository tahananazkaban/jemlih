// Package documents keeps identity documents in a private S3 bucket that browsers reach only through presigned URLs.
package documents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNoObject means nothing was uploaded under the key.
var ErrNoObject = errors.New("no object under this key")

// Bucket stores the objects; presign signs URLs for the address browsers use, which may differ from the service's.
type Bucket struct {
	S3        *s3.Client
	Presign   *s3.PresignClient
	Name      string
	UploadTTL time.Duration
	ReadTTL   time.Duration
}

// Object describes a stored file.
type Object struct {
	ContentType string
	Size        int64
}

// Link is a presigned URL and the moment it stops working.
type Link struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// UploadLink lets a browser PUT one file of contentType to key, and nothing else.
func (b *Bucket) UploadLink(ctx context.Context, key, contentType string) (Link, error) {
	req, err := b.Presign.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &b.Name, Key: &key, ContentType: &contentType}, s3.WithPresignExpires(b.UploadTTL))
	if err != nil {
		return Link{}, fmt.Errorf("presign upload: %w", err)
	}
	// Content-Type is signed, so the browser must send exactly this value.
	return Link{URL: req.URL, Headers: map[string]string{"Content-Type": contentType}, ExpiresAt: time.Now().Add(b.UploadTTL).UTC()}, nil
}

// ReadLink lets the holder GET key until the read TTL runs out.
func (b *Bucket) ReadLink(ctx context.Context, key string) (Link, error) {
	req, err := b.Presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &b.Name, Key: &key}, s3.WithPresignExpires(b.ReadTTL))
	if err != nil {
		return Link{}, fmt.Errorf("presign read: %w", err)
	}
	return Link{URL: req.URL, ExpiresAt: time.Now().Add(b.ReadTTL).UTC()}, nil
}

// Head returns what is stored under key, or ErrNoObject.
func (b *Bucket) Head(ctx context.Context, key string) (Object, error) {
	out, err := b.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.Name, Key: &key})
	var missing *types.NotFound
	if errors.As(err, &missing) {
		return Object{}, ErrNoObject
	}
	if err != nil {
		return Object{}, fmt.Errorf("head %s: %w", key, err)
	}
	return Object{ContentType: aws.ToString(out.ContentType), Size: aws.ToInt64(out.ContentLength)}, nil
}

// Copy duplicates an object within the bucket, keeping its content type.
func (b *Bucket) Copy(ctx context.Context, from, to string) error {
	if _, err := b.S3.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &b.Name, Key: &to, CopySource: aws.String(b.Name + "/" + from)}); err != nil {
		return fmt.Errorf("copy %s: %w", from, err)
	}
	return nil
}

// Delete removes key; deleting a missing key is not an error.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	if _, err := b.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.Name, Key: &key}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}
