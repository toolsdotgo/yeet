package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"text/template"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/toolsdotgo/sfm/pkg/sfm"
	"gopkg.in/yaml.v2"
)

var version = "edge"
var platform = "unknown"

//go:embed yeet-cf/ecs.yml.tpl
var ecstpl string

//go:embed yeet-cf/params/default.yml
var defaults string

var bk bool
var cfg aws.Config
var c command

type command struct {
	cfnc *cloudformation.Client // cloudformation client
	ssmc *ssm.Client            // ssm client
}

func main() {
	// yeet [-h|-v]
	fhelp := flag.Bool("h", false, "show help")
	fver := flag.Bool("v", false, "show version")
	freg := flag.String("r", "", "set aws region")
	flag.BoolVar(&bk, "bk", false, "force running as though in Buildkite")

	flag.Parse()

	// yeet deploy [param_files ...]
	fsDeploy := flag.NewFlagSet("deploy", flag.ExitOnError)
	fDeployHelp := fsDeploy.Bool("h", false, "show help for deploy")
	fDeployTagsfile := fsDeploy.String("tf", "", "tag file for CloudFormation Stack")

	// yeet output [subcommand]
	fsOutput := flag.NewFlagSet("output", flag.ExitOnError)
	fOutputHelp := fsOutput.Bool("h", false, "show help for output")

	if *fver {
		fmt.Println(version, platform)
		os.Exit(0)
	}
	if flag.NArg() == 0 || *fhelp {
		fmt.Print(usageTop)
		os.Exit(64)
	}
	region := *freg
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		fmt.Fprintln(os.Stderr, "no region set - as flag or env var")
		os.Exit(1)
	}

	var err error
	cfg, err = config.LoadDefaultConfig(
		context.TODO(),
		config.WithRegion(region),
		config.WithRetryer(func() aws.Retryer {
			retryer := retry.AddWithMaxAttempts(retry.NewStandard(), 10)
			return retry.AddWithMaxBackoffDelay(retryer, 30*time.Second)
		}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get aws config: %v\n", err)
		os.Exit(1)
	}
	c.cfnc = cloudformation.NewFromConfig(cfg)
	c.ssmc = ssm.NewFromConfig(cfg)

	if os.Getenv("BUILDKITE") == "true" {
		bk = true
	}

	switch flag.Arg(0) {
	case "deploy":
		_ = fsDeploy.Parse(flag.Args()[1:])
	case "output":
		_ = fsOutput.Parse(flag.Args()[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand '%s'\n", flag.Arg(0))
		fmt.Print(usageTop)
		os.Exit(64)
	}

	if fsDeploy.Parsed() {
		if *fDeployHelp {
			fmt.Print(usageDeploy)
			os.Exit(64)
		}
		os.Exit(c.deployYeet(fsDeploy.Args(), region, *fDeployTagsfile))
	}
	if fsOutput.Parsed() {
		if *fOutputHelp {
			fmt.Print(usageOutput)
			os.Exit(64)
		}
		switch flag.Arg(1) {
		case "template":
			tpl, err := generateTemplate(ecstpl, defaults, flag.Args()[2:], region)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed generate template: %v", err)
				os.Exit(1)
			}
			fmt.Println(tpl)
		case "inputs":
			values, err := readValues(defaults, flag.Args()[2:], region)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to read values: %v", err)
				os.Exit(1)
			}
			bb, err := yaml.Marshal(values)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to marshal inputs: %v", err)
				os.Exit(1)
			}
			fmt.Println(string(bb))
		case "running":
			values, err := readValues(defaults, flag.Args()[2:], region)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to read values: %v", err)
				os.Exit(1)
			}
			stackname, ok := values["name"].(string) // sorry
			if !ok {
				fmt.Fprintf(os.Stderr, "no stack name found")
				os.Exit(1)
			}

			h := sfm.Handle{CFNcli: c.cfnc}
			stack := h.NewStack(stackname)

			if stack.Created.IsZero() {
				fmt.Fprintf(os.Stderr, "stack doesn't exist")
				os.Exit(1)
			}
			_, err = describeService(stack)
			if err != nil {
				fmt.Fprintf(os.Stderr, "cant describe service: %v", err)
				os.Exit(1)
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown output subcommand '%s'\n", flag.Arg(1))
			fmt.Print(usageTop)
			os.Exit(64)
		}
	}
}

func (c command) deployYeet(args []string, region string, tagsfile string) int {
	template, err := generateTemplate(ecstpl, defaults, args, region)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed generate template: %v", err)
		return 1
	}

	values, err := readValues(defaults, args, region)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read values: %v", err)
		return 1
	}
	stackname, ok := values["name"].(string) // sorry
	if !ok {
		fmt.Fprintf(os.Stderr, "no stack name found")
		return 1
	}

	h := sfm.Handle{CFNcli: c.cfnc}
	stack := h.NewStack(stackname)

	if !stack.Created.IsZero() {
		if bk {
			fmt.Println("+++ Describe running ECS Tasks before deployment")
		}
		_, err = describeService(stack)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant describe service pre-update: %v\n", err)
		}
	}
	fmt.Println()

	if bk {
		fmt.Println("+++ Deploying Yeet Stack")
	}

	if err := stack.NewTemplate([]byte(template)); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load template into stack: %v", err)
		return 1
	}

	stack.Tags, err = loadTags(tagsfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant load tags: %v", err)
		return 1
	}

	timeout := 60 * time.Minute
	token, err := h.Make(stack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to make stack: %v", err)
		return 1
	}

	id := ""
	for start := time.Now(); time.Since(start) < timeout; {
		s, err := h.Get(stackname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant get stack: %v", err)
			return 1
		}
		ee, err := s.Events(id, token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant get events: %v", err)
			return 1
		}
		for _, e := range ee {
			fmt.Print(e.Pretty())
			id = e.ID
		}
		if s.Short == "ok" {
			if bk {
				fmt.Println("+++ Describe running ECS Tasks after deployment")
			}
			_, err = describeService(s)
			if err != nil {
				fmt.Fprintf(os.Stderr, "cant describe service post-update: %v", err)
			}
			return 0
		}
		if s.Short == "err" {
			fmt.Fprintf(os.Stderr, "stack in err state: %v\n", s.Status)
			if bk {
				fmt.Println("+++ Describe running ECS Tasks after failed deployment")
			}
			_, err = describeService(s)
			if err != nil {
				fmt.Fprintf(os.Stderr, "cant describe service post-update: %v", err)
			}
			return 1
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "stack operation wait timed out, took longer than %s\n", timeout)
	if bk {
		fmt.Println("+++ Describe running ECS Tasks after timedout deployment")
	}
	stack, err = h.Get(stackname)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get stack: %v", err)
		return 1
	}
	_, err = describeService(stack)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant describe service post-update: %v", err)
	}
	return 1
}

func describeService(s sfm.Stack) (string, error) {
	client := ecs.NewFromConfig(cfg)

	serviceArn := s.Outputs["Service"]
	if serviceArn == "" {
		return "", fmt.Errorf("no service in stack outputs")
	}

	clusterArn := s.Outputs["Cluster"]
	if clusterArn == "" {
		return "", fmt.Errorf("no cluster in stack outputs")
	}

	service, err := client.DescribeServices(context.TODO(), &ecs.DescribeServicesInput{
		Cluster:  aws.String(clusterArn),
		Include:  []types.ServiceField{"TAGS"},
		Services: []string{serviceArn},
	})
	if err != nil {
		return "", fmt.Errorf("failed to get service: %v", err)
	}
	if len(service.Services) != 1 {
		return "", fmt.Errorf("only a single ECS Service should be returned, %v found", len(service.Services))
	}
	taskARNs, err := client.ListTasks(context.TODO(), &ecs.ListTasksInput{
		Cluster:     aws.String(clusterArn),
		ServiceName: service.Services[0].ServiceName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get task ARNs: %v", err)
	}
	if len(taskARNs.TaskArns) < 1 {
		fmt.Println("No tasks were found running, was this intentional?")
		return "", nil
	}
	tasks, err := client.DescribeTasks(context.TODO(), &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterArn),
		Include: []types.TaskField{"TAGS"},
		Tasks:   taskARNs.TaskArns,
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe tasks: %v", err)
	}
	taskDef := make(map[string]string)
	fmt.Println("Running Tasks:")
	fmt.Println(" #   | Task ID                          | Task Version                        | Created at")
	fmt.Println("-----+----------------------------------+-------------------------------------+-----------------------------------")
	for i, t := range tasks.Tasks {
		p := strings.LastIndex(*t.TaskArn, "/")
		arn := *t.TaskArn
		id := arn[p+1:]
		p = strings.LastIndex(*t.TaskDefinitionArn, "/")
		def := *t.TaskDefinitionArn
		vers := def[p+1:]
		taskDef[def] = vers
		loc, _ := time.LoadLocation("Local") // WARN this might break on non-UNIX systems
		s := "PROVISIONING"
		if *t.LastStatus != "PROVISIONING" {
			s = t.CreatedAt.In(loc).String()
		}
		c := fmt.Sprintf("%v", i+1)
		fmt.Printf(" %3.3s | %s | %-35s | %s\n", c, id, vers, s)
	}
	fmt.Println()
	fmt.Println("Active Task Definitions:")
	for td, vers := range taskDef {
		def, err := client.DescribeTaskDefinition(context.TODO(), &ecs.DescribeTaskDefinitionInput{
			Include:        []types.TaskDefinitionField{"TAGS"},
			TaskDefinition: &td,
		})
		if err != nil {
			return "", fmt.Errorf("failed to describe task definition (%v): %v", td, err)
		}
		fmt.Println(" Task Version                       | CPU  | Mem  | Date")
		fmt.Println("------------------------------------+------+------+-----------------------------------")
		cpu := *def.TaskDefinition.Cpu
		mem := *def.TaskDefinition.Memory
		loc, _ := time.LoadLocation("Local") // WARN this might break on non-UNIX systems
		date := def.TaskDefinition.RegisteredAt.In(loc)
		fmt.Printf(" %-35s| %4s | %4s | %s\n", vers, cpu, mem, date)
		fmt.Println()
		fmt.Printf("Containers for %v\n", vers)
		fmt.Println(" Name                                | Image")
		fmt.Println("-------------------------------------+-------------------------------------")
		for _, c := range def.TaskDefinition.ContainerDefinitions {
			fmt.Printf(" %-35s | %s\n", *c.Name, *c.Image)
		}
	}
	return "", nil
}

func generateTemplate(tpl_string string, defaults string, param_files []string, region string) (string, error) {
	funcMap := template.FuncMap{
		"add": func(i int, b int) int {
			return i + b
		},
		"rand": func(len int) string {
			buff := make([]byte, len)
			if _, err := rand.Read(buff); err != nil {
				panic(err)
			}
			return hex.EncodeToString(buff)
		},
		"resolvessm": func(format string, input ...interface{}) string {
			ssmFormat := fmt.Sprintf("{{resolve:ssm:%v}}", format)
			return fmt.Sprintf(ssmFormat, input...)
		},
		"suffix": func(input string, sep string) string {
			s := strings.Split(input, sep)
			return s[len(s)-1]
		},
		"trimws": func(input string) string {
			s := strings.ReplaceAll(input, " ", "")
			return s
		},
		"logicalid": func(input ...string) string {
			var s string
			var re = regexp.MustCompile("[^A-Za-z0-9]+")
			for _, i := range input {
				s = fmt.Sprintf("%s%s", s, re.ReplaceAllString(i, ""))
			}
			return s
		},
		"titlecase": func(input string) string {
			if strings.ToLower(input) == "allow" {
				return "Allow"
			}
			return "Deny"
		},
		"rangestart": func(input interface{}) string {
			re := regexp.MustCompile(`^(-?[^\-]+)`)
			res := re.FindAllStringSubmatch(fmt.Sprint(input), 1)
			for i := range res {
				return fmt.Sprint(res[i][1])
			}
			return ""
		},
		"rangeend": func(input interface{}) string {
			re := regexp.MustCompile(`^([^\\-]-)?(-?[^\\-]+)$`)
			res := re.FindAllStringSubmatch(fmt.Sprint(input), 1)
			for i := range res {
				return fmt.Sprint(res[i][2])
			}
			return ""
		},
		"contains": func(s, substr string) bool {
			return strings.Contains(s, substr)
		},
	}

	tpl, err := template.New("ecs").Option("missingkey=zero").Funcs(funcMap).Parse(tpl_string)
	if err != nil {
		return "", fmt.Errorf("error parsing template: %v", err)
	}

	values, err := readValues(defaults, param_files, region)
	if err != nil {
		return "", fmt.Errorf("failed to read values: %v", err)
	}

	buf := new(bytes.Buffer)
	err = tpl.Execute(buf, values)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %v", err)
	}
	regex, err := regexp.Compile("\n\\s*\n")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to compile blank line regex: %v", err)
		return buf.String(), nil
	}
	return regex.ReplaceAllString(buf.String(), "\n"), nil
}

func readValues(defaults string, filenames []string, region string) (map[string]interface{}, error) {
	// initialise some variables
	resultMap := make(map[string]interface{})
	var defaultMap map[string]interface{}
	var err error

	// load the defaults in
	if err := yaml.Unmarshal([]byte(defaults), &defaultMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %v", err)
	}

	resultMap, err = loadFiles(resultMap, filenames)
	if err != nil {
		return nil, fmt.Errorf("unable to load config files: %v", err)
	}

	resultMap, err = loadIncludes(resultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to load _includes: %v", err)
	}

	if awsConfig, ok := resultMap["aws"]; ok {
		awsConfig := assertMSI(awsConfig)
		if awsConfig == nil {
			return nil, fmt.Errorf("Yeet's `.aws` config key seems malformed")
		}
		if _, ok := awsConfig["region"]; !ok {
			awsregionMap := map[string]interface{}{
				"aws": map[string]interface{}{
					"region": region,
				},
			}
			resultMap, err = mergeKeys(resultMap, awsregionMap)
			if err != nil {
				return nil, fmt.Errorf("unable to merge aws region in to config: %v", err)
			}
		}
	}

	resultMap, err = mergeKeys(resultMap, defaultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to merge yeet defaults: %v", err)
	}

	resultMap, err = templateConfig(resultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to template config: %v", err)
	}

	resultMap, err = defaultKeys(resultMap)
	if err != nil {
		return nil, fmt.Errorf("unable to load default keys: %v", err)
	}

	resultMap = deleteNulls(resultMap)

	return resultMap, nil
}

func loadFiles(resultMap map[string]interface{}, filenames []string) (map[string]interface{}, error) {
	for _, f := range filenames {
		var fileValues map[string]interface{}
		bs, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %v", err)
		}
		if err := yaml.Unmarshal(bs, &fileValues); err != nil {
			return nil, fmt.Errorf("failed to unmarshal yaml: %v", err)
		}
		resultMap, err = mergeKeys(resultMap, fileValues)
		if err != nil {
			return nil, fmt.Errorf("unable to merge config files: %v", err)
		}
	}
	return resultMap, nil
}

func loadSSM(resultMap map[string]interface{}, param string) (map[string]interface{}, error) {
	ssmparam, err := c.ssmc.GetParameter(
		context.TODO(),
		&ssm.GetParameterInput{
			Name:           aws.String(param),
			WithDecryption: aws.Bool(true),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("unable to get param %v: %v", param, err)
	}
	var pm map[string]interface{}
	if err := yaml.Unmarshal([]byte(*ssmparam.Parameter.Value), &pm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ssm param %v yaml: %v", param, err)
	}

	resultMap, err = mergeKeys(resultMap, pm)
	if err != nil {
		return nil, fmt.Errorf("unable to merge ssm param %v: %v", param, err)
	}

	return resultMap, nil
}

// Given two maps, recursively merge right into left, NEVER replacing any key that already exists in left
func mergeKeys(left map[string]interface{}, right map[string]interface{}) (map[string]interface{}, error) {
	var err error
	for key, rightVal := range right {
		if leftVal, ok := left[key]; ok {
			// _include is an array which we always want to append to so we don't lose any configs to include
			if key == "_include" {
				left[key], err = mergeSS(left[key], rightVal)
				if err != nil {
					return nil, fmt.Errorf("unable to merge _includes: %v", err)
				}
				continue
			}
			// then we don't want to replace it - do we want to recurse?
			leftVal := assertMSI(leftVal)
			if leftVal == nil {
				// don't recurse, the existing left value wins
				continue
			}

			rightVal := assertMSI(rightVal)
			if rightVal == nil {
				// don't recurse, the existing left value wins (it's already set and might be more "complex")
				continue
			}

			left[key], err = mergeKeys(leftVal, rightVal)
			if err != nil {
				return nil, fmt.Errorf("unable to merge configs: %v", err)
			}
			continue
		}
		// key not in left so we can just shove it in
		left[key] = rightVal
	}
	return left, nil
}

func mergeSS(si ...interface{}) ([]string, error) {
	var rs []string

	// make one long slice string
	for _, i := range si {
		ss, err := assertSS(i)
		if err != nil {
			return nil, fmt.Errorf("unable to merge slices of string from left: %v", err)
		}
		rs = append(rs, ss...)
	}

	if len(rs) <= 1 {
		return rs, nil
	}

	// deduplicate the list
	result := []string{}
	seen := make(map[string]struct{})
	for _, s := range rs {
		if _, ok := seen[s]; !ok {
			result = append(result, s)
			seen[s] = struct{}{}
		}
	}
	return result, nil
}

// Recurse through provided config map[string]interface{} and merge values from '_defaults' keys into maps
func defaultKeys(config map[string]interface{}) (map[string]interface{}, error) {
	var err error
	// if _defaults is in the map merge them in
	if d, ok := config["_defaults"]; ok {
		defaults := assertMSI(d)
		if defaults == nil {
			return nil, fmt.Errorf("value in _defaults must be a map")
		}
		for k, v := range config {
			if k == "_defaults" {
				continue
			}
			left := assertMSI(v)
			if left == nil {
				// value at k isn't a map, no need for defaults
				continue
			}
			// merge the defaults in to the config at k
			config[k], err = mergeKeys(left, defaults)
			if err != nil {
				return nil, fmt.Errorf("unable to merge _defaults: %v", err)
			}
		}
	}
	// loop through the keys in the map
	for k, v := range config {
		if k == "_defaults" {
			config[k] = nil
			continue
		}
		v := assertMSI(v)
		if v == nil {
			// value at k isn't a map, no need for defaults
			continue
		}
		// recurse the map at k using map[string]interface{}'d values v
		config[k], err = defaultKeys(v)
		if err != nil {
			return nil, fmt.Errorf("unable to process defaults at key: %v.\n%v", k, err)
		}
	}
	return config, nil
}

// Convert a provided interface{} into a map[string]interface{} or return nil
func assertMSI(m interface{}) map[string]interface{} {
	switch s := m.(type) {
	case map[string]interface{}:
		// can be asserted directly
		return s
	case map[interface{}]interface{}:
		// needs to convert each key to string individually
		n := make(map[string]interface{})
		for k, lv := range s {
			n[fmt.Sprintf("%v", k)] = lv
		}
		return n
	default:
		// it's not a map
		return nil
	}
}

// Delete keys who's value is null
func deleteNulls(config map[string]interface{}) map[string]interface{} {
	for k, v := range config {
		if v == nil {
			delete(config, k)
			continue
		}
		v := assertMSI(v)
		if v == nil {
			// value at k isn't a map, no need to check for nulls
			continue
		}
		// recurse the map at k using map[string]interface{}'d values v
		config[k] = deleteNulls(v)
	}
	return config
}

func templateConfig(config map[string]interface{}) (map[string]interface{}, error) {
	var templateConfig string
	var lastLoopConfig string

	// loop for up to 10 times so redirected template values get resolved
	for i := 1; i < 10; i++ {
		b, err := yaml.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal config: %v", err)
		}
		templateConfig = string(b)
		if templateConfig == lastLoopConfig {
			// the template stopped changing, break the loop
			break
		}
		lastLoopConfig = templateConfig

		funcMap := template.FuncMap{
			"json": func(input interface{}) string {
				j, err := json.Marshal(input)
				if err != nil {
					fmt.Fprintf(os.Stderr, "unable to marshal to json: %v\n", err)
					panic(err)
				}
				return string(j)
			},
			"join": func(input interface{}, sep string) string {
				var ss []string
				switch input := input.(type) {
				case []string:
					ss = input
				case []interface{}:
					for _, s := range input {
						if s != nil {
							ss = append(ss, fmt.Sprint(s))
						}
					}
				default:
					fmt.Fprintf(os.Stderr, "unsupported interface: %v\n", input)
					panic("unsupported interface")
				}
				return strings.Join(ss, sep)
			},
			"ssm": func(param string) string {
				ssmparam, err := c.ssmc.GetParameter(
					context.TODO(),
					&ssm.GetParameterInput{
						Name:           aws.String(param),
						WithDecryption: aws.Bool(true),
					},
				)
				if err != nil {
					fmt.Fprintf(os.Stderr, "unable to get param %v: %v", param, err)
					panic("unable to get param value")
				}
				return *ssmparam.Parameter.Value
			},
		}

		tpl, err := template.New("config").Option("missingkey=error").Delims("<(", ")>").Funcs(funcMap).Parse(templateConfig)
		if err != nil {
			return nil, fmt.Errorf("error parsing config as template: %v", err)
		}

		buf := new(bytes.Buffer)
		err = tpl.Execute(buf, config)
		if err != nil {
			return nil, fmt.Errorf("failed to execute template: %v", err)
		}
		stringConfig := buf.String()

		if err := yaml.Unmarshal([]byte(stringConfig), &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal yaml: %v", err)
		}
	}

	return config, nil
}

func assertSS(i interface{}) ([]string, error) {
	switch it := i.(type) {
	case []string:
		return it, nil
	case []interface{}:
		// needs to convert each item to string individually
		var ss []string
		for _, v := range it {
			s := fmt.Sprintf("%v", v)
			ss = append(ss, s)
		}
		return ss, nil
	default:
		return nil, fmt.Errorf("expected slice of strings/an array of strings, you gave %T", it)
	}
}

func loadIncludes(config map[string]interface{}) (map[string]interface{}, error) {
	if config["_include"] == nil {
		return config, nil
	}

	loadedIncludes := map[string]struct{}{}
	// loop for up to 10 times to attempt to load all includes but trying to avoid any _include loops
	for i := 1; i < 10; i++ {
		includes, err := assertSS(config["_include"])
		if err != nil {
			return nil, fmt.Errorf("unable to get list of _include files: %v", err)
		}
		newIncludes := false
		for _, inc := range includes {
			if _, ok := loadedIncludes[inc]; ok {
				continue
			}
			newIncludes = true
			if len(inc) >= 6 && strings.HasPrefix(inc, "ssm://") {
				config, err = loadSSM(config, strings.TrimPrefix(inc, "ssm://"))
				if err != nil {
					return nil, fmt.Errorf("unable to load ssm param: %v", err)
				}
				continue
			}
			config, err = loadFiles(config, []string{inc})
			if err != nil {
				return nil, fmt.Errorf("unable to load _include files: %v", err)
			}
			loadedIncludes[inc] = struct{}{}
		}
		if !newIncludes {
			break
		}
	}
	return config, nil
}

func loadTags(fn string) (map[string]string, error) {
	if fn == "" {
		// You didn't give me a file path so I won't do anything
		return map[string]string{}, nil
	}
	bb, err := os.ReadFile(filepath.Clean(fn))
	if err != nil {
		return nil, fmt.Errorf("can't read file %s: %w", fn, err)
	}

	var i interface{}
	err = yaml.Unmarshal(bb, &i)
	if err != nil {
		return nil, fmt.Errorf("can't unmarshal file %s: %w", fn, err)
	}

	res := map[string]string{}
	// TODO panics on bad input, add check
	for k, v := range i.(map[interface{}]interface{}) {
		key := k.(string)
		// handle normal 'key: value`
		if val, ok := v.(string); ok {
			res[key] = val
			continue
		}
		// handle yaml thinking this `key: value` was a bool (yaml.v2 treats `yes/no`, `true/false`, `on/off` as bool)
		if val, ok := v.(bool); ok {
			if val {
				res[key] = "True"
				continue
			}
			res[key] = "False"
			continue
		}
		// handle lists of values, 'key: \n  - value 1\n  - value 2'
		if valslice, ok := v.([]interface{}); ok {
			var vals []string
			for _, valinterface := range valslice {
				if val, ok := valinterface.(string); ok {
					vals = append(vals, val)
				}
				if val, ok := v.(bool); ok {
					if val {
						vals = append(vals, "True")
					}
					vals = append(vals, "False")
				}
			}
			csv := strings.Join(vals, ",")
			res[key] = csv
			continue
		}
		return nil, fmt.Errorf("can't load in file, something wrong with key %s", key)
	}
	return res, nil
}

const usageTop = `██╗░░░██╗███████╗███████╗████████╗
╚██╗░██╔╝██╔════╝██╔════╝╚══██╔══╝
░╚████╔╝░█████╗░░█████╗░░░░░██║░░░
░░╚██╔╝░░██╔══╝░░██╔══╝░░░░░██║░░░
░░░██║░░░███████╗███████╗░░░██║░░░
░░░╚═╝░░░╚══════╝╚══════╝░░░╚═╝░░░


Usage
  yeet [-h|-v] [-r <region>] [subcommand]

  -h  display this help
  -v  display the version
  -r  set the aws region manually

Sub-Commands
  deploy    deploy a yeet stack
  output    output info about a yeet stack

  use <subcommand> -h for subcommand-specific help

Examples
  TODO
`

const usageDeploy = `yeet deploy [-tf ./tags.yml] <yeet-config.yml ...>

Summary
  manages the deployment of the Yeet CloudFormation Stack

Flags
  -tf <file>        a path to a yaml file containing tags
  <yeet-config.yml> a path to one of more yaml files
                    containing the config for the stack
`

const usageOutput = `yeet output [inputs|running|template]
TODO
`
