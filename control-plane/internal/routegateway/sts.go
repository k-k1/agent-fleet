package routegateway

// The CP's own identity taking the route role for one membership (ADR
// 0129 D1b, D5): the ECS task role (CpTaskRole) or IRSA identity, from the default credential
// chain of the CP's process. Nothing is stored. The route role's trust policy must allow this
// identity sts:AssumeRole and sts:TagSession (deploy/aws/ecs/cfn).

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

// routeSessionDuration is the assumed session's life. One hour is AssumeRole's default and the
// role's default maximum; the cache replaces it routeCredReserve before the end.
const routeSessionDuration = time.Hour

// roleSessionNameRe is AWS's RoleSessionName alphabet and length. A membership id is hex, so a
// mismatch is a defect, refused rather than truncated (a cut name would attribute to another).
var roleSessionNameRe = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)

type stsAssumer struct {
	mu      sync.Mutex
	clients map[string]*sts.Client // by Region: the regional STS endpoint of the call's Region
}

// NewSTSAssumer takes the route role with the CP process's default AWS credential chain.
func NewSTSAssumer() Assumer {
	return &stsAssumer{clients: map[string]*sts.Client{}}
}

func (a *stsAssumer) client(ctx context.Context, region string) (*sts.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[region]; ok {
		return c, nil
	}
	cfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if err != nil {
		return nil, err
	}
	c := sts.NewFromConfig(cfg)
	a.clients[region] = c
	return c, nil
}

func (a *stsAssumer) Assume(ctx context.Context, in AssumeInput) (aws.Credentials, error) {
	if !roleSessionNameRe.MatchString(in.SessionName) {
		return aws.Credentials{}, errors.New("the membership id is not a valid role session name")
	}
	c, err := a.client(ctx, in.Region)
	if err != nil {
		return aws.Credentials{}, err
	}
	keys := make([]string, 0, len(in.Tags))
	for k := range in.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var tags []ststypes.Tag
	for _, k := range keys {
		tags = append(tags, ststypes.Tag{Key: aws.String(k), Value: aws.String(in.Tags[k])})
	}
	req := &sts.AssumeRoleInput{
		RoleArn:         aws.String(in.RoleARN),
		RoleSessionName: aws.String(in.SessionName),
		Policy:          aws.String(in.Policy),
		Tags:            tags,
		DurationSeconds: aws.Int32(int32(routeSessionDuration / time.Second)),
	}
	if in.ExternalID != "" {
		req.ExternalId = aws.String(in.ExternalID)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := c.AssumeRole(ctx, req)
	if err != nil {
		return aws.Credentials{}, err
	}
	if out.Credentials == nil {
		return aws.Credentials{}, errors.New("AssumeRole answered no credentials")
	}
	return aws.Credentials{
		AccessKeyID: aws.ToString(out.Credentials.AccessKeyId), SecretAccessKey: aws.ToString(out.Credentials.SecretAccessKey),
		SessionToken: aws.ToString(out.Credentials.SessionToken), Source: "af-provided-route",
		CanExpire: true, Expires: aws.ToTime(out.Credentials.Expiration),
	}, nil
}
