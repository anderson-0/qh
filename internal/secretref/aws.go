package secretref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

	"github.com/anderson-0/qh/internal/qerr"
)

type smAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// awsResolver reads AWS Secrets Manager with the qh process's default
// credential chain. Clients are made once per region.
type awsResolver struct {
	newClient func(ctx context.Context, region string) (smAPI, error)
	mu        sync.Mutex
	clients   map[string]smAPI
}

func newAWS() Resolver {
	return &awsResolver{newClient: func(ctx context.Context, region string) (smAPI, error) {
		var opts []func(*config.LoadOptions) error
		if region != "" {
			opts = append(opts, config.WithRegion(region))
		}
		cfg, err := config.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return secretsmanager.NewFromConfig(cfg), nil
	}}
}

// regionOf returns the region of a Secrets Manager ARN, or "" for a name.
func regionOf(id string) string {
	parts := strings.Split(id, ":")
	if len(parts) >= 4 && parts[0] == "arn" && parts[2] == "secretsmanager" {
		return parts[3]
	}
	return ""
}

func (a *awsResolver) client(ctx context.Context, region string) (smAPI, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[region]; ok {
		return c, nil
	}
	c, err := a.newClient(ctx, region)
	if err != nil {
		return nil, err
	}
	if a.clients == nil {
		a.clients = map[string]smAPI{}
	}
	a.clients[region] = c
	return c, nil
}

func (a *awsResolver) Resolve(ctx context.Context, r Ref) (string, error) {
	c, err := a.client(ctx, regionOf(r.Name))
	if err != nil {
		return "", qerr.New(qerr.ConnectionFailed, "secret reference "+r.Raw+": cannot load the AWS configuration").
			WithHint("Check the AWS credentials and region of the qh process")
	}
	out, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(r.Name)})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			switch ae.ErrorCode() {
			case "ResourceNotFoundException":
				return "", failed(r.Raw, "not found")
			case "AccessDeniedException":
				return "", failed(r.Raw, "access denied")
			case "DecryptionFailure":
				return "", failed(r.Raw, "cannot be decrypted")
			}
			return "", failed(r.Raw, "AWS error "+ae.ErrorCode())
		}
		return "", qerr.New(qerr.ConnectionFailed, "secret reference "+r.Raw+": cannot reach AWS Secrets Manager").
			WithHint("Check the AWS credentials and region of the qh process")
	}
	if out.SecretString == nil {
		return "", failed(r.Raw, "the secret is binary; only string secrets are supported")
	}
	if r.Key == "" {
		return *out.SecretString, nil
	}
	dec := json.NewDecoder(strings.NewReader(*out.SecretString))
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) != nil || dec.Decode(new(any)) != io.EOF {
		return "", failed(r.Raw, "the secret is not JSON, so #"+r.Key+" cannot be read")
	}
	v, ok := fields[r.Key]
	if !ok {
		return "", failed(r.Raw, "no such key")
	}
	switch v := v.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		return fmt.Sprint(v), nil
	}
	return "", failed(r.Raw, "field "+r.Key+" is not a string, number or boolean")
}
