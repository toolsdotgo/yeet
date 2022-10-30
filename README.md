# yeet

Yeet launches Docker containers into AWS ECS/Fargate

## AWS Account Dependencies

There's a few resources and configuration Yeet expects to be in place. The easiest way to get this it to deploy `cf/infra.yml` using [sfm](https://github.com/toolsdotgo/sfm) into your chosen account and region.

## Yeet Config

For a reference of what each config item means for Yeet see the [Config Reference](./docs/config-reference.yml).

### Config Example

A very full example might look something like this.

This would create an ECS Service attached to an ALB using path matching and an NLB. The Tasks will be running multiple containers with shared volumes available between one of the sidecars and the "main app".

It's likely that parts of this would be pulled out in to generic configs that could be `_include`'d in, either environment-specific/app-generic (possibly stored in an SSM Parameter) or app-specific/environment-generic which could be `_include`'d in from another YAML file.

It's also possible to set defaults inside maps in the config using the `_defaults` key which will then be applied across all of the other keys inside that map.

```yaml
---
aws:
  application_load_balancers:
    api:
      connection_draining_timeout: 60
      container:
        name: my-api # default $.name
        port: 8443
      health_check:
        healthy_threshold: 2
        interval: 30
        path: /health
        protocol: HTTPS
        timeout: 5
        unhealthy_threshold: 3
      listener_rules:
        internal:
          listener_arn: arn:aws:elasticloadbalancing:ap-southeast-2:1234567890:listener/app/my-api-alb/abcdef/ghijkl
          path: /my-path/to/my-api
      protocol: HTTPS
      # target_group: 
  ecs:
    cluster: arn:aws:ecs:ap-southeast-2:1234567890:cluster/my-api-cluster
    deployment:
      maximum_percent: 200
      minimum_healthy_percent: 100
      timeout: PT10M # default: PT15M
    platform_version: "1.3.0" # default: 1.4.0
    task:
      cpu: 256
      execution_role: yeet-ExecutionRole-ABCD1234
      memory: 512
      security_groups:
        - sg-abcd1234 # for BYO security group, default: null
      subnets:
        - subnet-fabc23
        - subnet-bcd567
  iam:
    # role_arn: '{{resolve:ssm:<($.name)>-task-role}}' # BYO role, can even have cloudformation pull it from a param store value
    role:
      policy_statements:
        cloudwatchlogs:
          effect: allow
          action:
            - logs:CreateLogStream
            - logs:PutLogEvents
          resource:
            - !Sub arn:*:logs:*:${AWS::AccountId}:log-group:<($.containers.my-app.logs.group)> # TODO: cloudformation intrinsic functions don't yet work
  network_load_balancers:
    my-api:
      access_logging:
        bucket: nlb-logs
        prefix: my-api
      connection_draining_timeout: 300
      container:
        name: my-app
        port: 8443
      cross_zone: true
      dns:
        my-api.example.com:
          weight: 100
          zone: example.com.
          # zone_id: ZONE_ID
      health_check:
        healthy_threshold: 2
        interval: 30
        path: /health
        port: 4443
        protocol: HTTPS
        unhealthy_threshold: 2
      port: 443
      protocol: TCP
      proxy_protocol_v2: true
      scheme: interet-facing
      stickiness: source_ip
      subnets:
        - subnet-abc123
        - subnet-def345
  region: ap-southeast-2 # default: value of region flag, AWS_REGION, or AWS_DEFAULT_REGION
  service_discovery:
    cloudmap:
      service: some-id
      namespace: some-namespace
      container: my-app
  vpc: vpc-abcdef098

containers:
  _defaults:
    logs:
      group: my-shared-app-log_group
  my-app:
    ports:
    - tcp: 443
    image: my-registry.example.com/myapp:1.2.3
    environment:
      S3_BUCKET: my-api-config-bucket
    logs:
      prefix: app # default: container name
      datetime: '%Y-%m-%d %H:%M:%S'
      region: ap-southeast-2 # default: aws.region
    readonly: true # default: false
    volumes_from:
      - container: sidecar-module
        readonly: true
    depends_on:
      - container: sidecar-module
        condition: SUCCESS
  sidecar-module:
    ecr:
      account: 9876543210
      region: ap-southeast-2 # default: aws.region
      repository: my-sidecar
      tag: 2.3.4-alpine

monitoring:
  cloudwatch:
    alarms:
      insufficientHealthyHosts:
        description: Fewer than the minimum number of Tasks are currently considered healthy
        dimensions:
          LoadBalancer: # TODO: how do you reference yeet resources?
          TargetGroup: # TODO: how do you reference yeet resources?
        notify:
          - arn:aws:sns:ap-southeast-2:1234567890:my_notify_topic
        notify_on:
          - alarm
          - ok
        period: 60
        times: 5
        treat_missing_data: missing
        when:
          comparison: LessThanThreshold
          metric: HealthyHostCount
          namespace: AWS/ApplicationELB
          statistic: Maximum
          # you can reference other values using Golang Text Templating with the delims '<(' and ')>'
          threshold: <($.scaling.min)>
    logs:
      retention: 14
      s3:
        bucket: my-log-bucket
        prefix: my-app
        kms: arn:aws:kms:ap-southeast-2:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab
        role: arn:aws:iam::111122223333:role/log-delivery-stream

name: my-api

scaling:
  desired: 1 # default: current running count or scaling.initial_count or scaling.min
  min: 1
  max: 3
  step_scaling:
    highCPU:
      adjustment: 1
      adjustment_type: ChangeInCapacity
      cooldown: 60
      description: Scale up Service when CPU above 80% for 5 minutes
      period: 60
      times: 5
      when:
        comparison: GreaterThanOrEqualToThreshold
        metric: CPUUtilization
        namespace: AWS/ECS
        statistic: Average
        threshold: 80

```
