package secretref

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"

	"github.com/anderson-0/qh/internal/qerr"
)

type fakeSM struct {
	region  string
	secrets map[string]*string
	err     error
}

func (f *fakeSM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.secrets[aws.ToString(in.SecretId)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "nope"}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: v}, nil
}

func TestAWSResolver(t *testing.T) {
	fake := &fakeSM{secrets: map[string]*string{
		"plain":    aws.String("pw-value"),
		"json":     aws.String(`{"password":"json-pw","port":5432}`),
		"nums":     aws.String(`{"id":12345678,"big":9007199254740993,"dec":1.50,"flag":true}`),
		"binary":   nil,
		"trailing": aws.String(`{"password":"x"} junk`),
		"arn:aws:secretsmanager:eu-west-1:123456789012:secret:x": aws.String("by-arn"),
	}}
	regions := map[string]bool{}
	r := &awsResolver{newClient: func(_ context.Context, region string) (smAPI, error) {
		regions[region] = true
		return fake, nil
	}}
	s := NewSet(false, nil).With("aws-sm", r)
	ctx := context.Background()
	for ref, want := range map[string]string{
		"aws-sm://plain":         "pw-value",
		"aws-sm://json#password": "json-pw",
		"aws-sm://json#port":     "5432",
		"aws-sm://nums#id":       "12345678",
		"aws-sm://nums#big":      "9007199254740993",
		"aws-sm://nums#dec":      "1.50",
		"aws-sm://nums#flag":     "true",
		"aws-sm://arn:aws:secretsmanager:eu-west-1:123456789012:secret:x": "by-arn",
	} {
		if v, err := s.Resolve(ctx, ref); err != nil || v != want {
			t.Errorf("%s: %q %v", ref, v, err)
		}
	}
	if len(regions) != 2 || !regions[""] || !regions["eu-west-1"] {
		t.Errorf("clients created for regions %v, want exactly \"\" and \"eu-west-1\"", regions)
	}
	for ref, msg := range map[string]string{
		"aws-sm://missing":           "not found",
		"aws-sm://json#nokey":        "no such key",
		"aws-sm://plain#field":       "not JSON",
		"aws-sm://binary":            "binary",
		"aws-sm://trailing#password": "not JSON",
	} {
		_, err := s.Resolve(ctx, ref)
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v", ref, err)
		}
		if err != nil && (strings.Contains(err.Error(), "pw-value") || strings.Contains(err.Error(), "json-pw")) {
			t.Errorf("%s leaked a value: %v", ref, err)
		}
	}
	fake.err = &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"}
	if _, err := s.Resolve(ctx, "aws-sm://plain"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("access denied: %v", err)
	}
}

func TestRegionOf(t *testing.T) {
	if got := regionOf("arn:aws:secretsmanager:us-east-2:1:secret:a"); got != "us-east-2" {
		t.Fatal(got)
	}
	if got := regionOf("prod/db"); got != "" {
		t.Fatal(got)
	}
}
