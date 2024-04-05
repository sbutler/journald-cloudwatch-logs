package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/exp/maps"

	"github.com/aws/aws-sdk-go/aws"
	awsCredentials "github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	awsSession "github.com/aws/aws-sdk-go/aws/session"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsimple"
	"github.com/zclconf/go-cty/cty"
)

type Config struct {
	AWSCredentials *awsCredentials.Credentials
	AWSRegion      string
	EC2InstanceId  string
	LogGroupName   string
	LogStreamName  string
	LogPriority    Priority
	StateFilename  string
	JournalDir     string
	BufferSize     int
}

type fileConfig struct {
	AWSRegion     string `hcl:"aws_region,optional"`
	EC2InstanceId string `hcl:"ec2_instance_id,optional"`
	LogGroupName  string `hcl:"log_group"`
	LogStreamName string `hcl:"log_stream,optional"`
	LogPriority   string `hcl:"log_priority,optional"`
	StateFilename string `hcl:"state_file"`
	JournalDir    string `hcl:"journal_dir,optional"`
	BufferSize    int    `hcl:"buffer_size,optional"`
}

func getLogLevel(priority string) (Priority, error) {

	logLevels := map[Priority][]string{
		EMERGENCY: {"0", "emerg"},
		ALERT:     {"1", "alert"},
		CRITICAL:  {"2", "crit"},
		ERROR:     {"3", "err"},
		WARNING:   {"4", "warning"},
		NOTICE:    {"5", "notice"},
		INFO:      {"6", "info"},
		DEBUG:     {"7", "debug"},
	}

	for i, s := range logLevels {
		if s[0] == priority || s[1] == priority {
			return i, nil
		}
	}

	return DEBUG, fmt.Errorf("'%s' is unsupported log priority", priority)
}

func LoadConfig(filename string) (*Config, error) {
	sess, err := awsSession.NewSession(&aws.Config{})
	if err != nil {
		return nil, fmt.Errorf("unable to create AWS session: %s", err)
	}
	metaClient := ec2metadata.New(sess)

	fConfig, err := readFileConfig(filename, metaClient)
	if err != nil {
		return nil, err
	}

	config := &Config{}

	if fConfig.AWSRegion != "" {
		config.AWSRegion = fConfig.AWSRegion
	} else {
		config.AWSRegion, err = metaClient.Region()
		if err != nil {
			return nil, fmt.Errorf("unable to detect AWS region: %s", err)
		}
	}

	if fConfig.EC2InstanceId != "" {
		config.EC2InstanceId = fConfig.EC2InstanceId
	} else {
		config.EC2InstanceId, err = metaClient.GetMetadata("instance-id")
		if err != nil {
			return nil, fmt.Errorf("unable to detect EC2 instance id: %s", err)
		}
	}

	if fConfig.LogPriority == "" {
		// Log everything
		config.LogPriority = DEBUG
	} else {
		config.LogPriority, err = getLogLevel(fConfig.LogPriority)
		if err != nil {
			return nil, fmt.Errorf("the provided log filtering '%s' is unsupported by systemd", fConfig.LogPriority)
		}
	}

	config.LogGroupName = fConfig.LogGroupName

	if fConfig.LogStreamName != "" {
		config.LogStreamName = fConfig.LogStreamName
	} else {
		// By default we use the instance id as the stream name.
		config.LogStreamName = config.EC2InstanceId
	}

	config.StateFilename = fConfig.StateFilename
	config.JournalDir = fConfig.JournalDir

	if fConfig.BufferSize != 0 {
		config.BufferSize = fConfig.BufferSize
	} else {
		config.BufferSize = 100
	}

	config.AWSCredentials = awsCredentials.NewChainCredentials([]awsCredentials.Provider{
		&awsCredentials.EnvProvider{},
		&ec2rolecreds.EC2RoleProvider{
			Client: metaClient,
		},
	})

	return config, nil
}

func (c *Config) NewAWSSession() (*awsSession.Session, error) {
	config := &aws.Config{
		Credentials: c.AWSCredentials,
		Region:      aws.String(c.AWSRegion),
		MaxRetries:  aws.Int(3),
	}
	return awsSession.NewSession(config)
}

func readFileConfig(filename string, metaClient *ec2metadata.EC2Metadata) (*fileConfig, error) {
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{},
	}

	for _, environEntry := range os.Environ() {
		environPair := strings.SplitN(environEntry, "=", 2)
		ctx.Variables["env."+environPair[0]] = cty.StringVal(environPair[1])
	}

	// If we can fetch the InstanceIdentityDocument then iterate over the
	// struct extracting the string fields and their values into the vars map
	document, err := metaClient.GetInstanceIdentityDocument()
	if err == nil {
		documentValues := map[string]cty.Value{
			"instance.devpayProductCodes":      stringListVal(document.DevpayProductCodes),
			"instance.marketplaceProductCodes": stringListVal(document.MarketplaceProductCodes),
			"instance.availabilityZone":        cty.StringVal(document.AvailabilityZone),
			"instance.privateIp":               cty.StringVal(document.PrivateIP),
			"instance.version":                 cty.StringVal(document.Version),
			"instance.region":                  cty.StringVal(document.Region),
			"instance.instanceId":              cty.StringVal(document.InstanceID),
			"instance.billingProducts":         stringListVal(document.BillingProducts),
			"instance.instanceType":            cty.StringVal(document.InstanceType),
			"instance.accountId":               cty.StringVal(document.AccountID),
			"instance.imageId":                 cty.StringVal(document.ImageID),
			"instance.kernelId":                cty.StringVal(document.KernelID),
			"instance.ramdiskId":               cty.StringVal(document.RamdiskID),
			"instance.architecture":            cty.StringVal(document.Architecture),
		}
		maps.Copy(ctx.Variables, documentValues)
	}

	var fConfig fileConfig
	err = hclsimple.DecodeFile(filename, ctx, &fConfig)
	if err != nil {
		return nil, err
	}
	return &fConfig, nil
}

func stringListVal(vals []string) cty.Value {
	if len(vals) == 0 {
		return cty.ListValEmpty(cty.String)
	}

	ctyVals := make([]cty.Value, len(vals))
	for i, val := range vals {
		ctyVals[i] = cty.StringVal(val)
	}
	return cty.ListVal(ctyVals)
}
