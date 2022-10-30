{{$r := rand 5 -}}
---
AWSTemplateFormatVersion: "2010-09-09"
Description: Template for {{$.name}}
Resources:
  TaskDefinition:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: '{{$.name}}'
      ContainerDefinitions:
      {{range $name, $c := $.containers}}
        - Name: '{{$name}}'
          Image: {{with $c.image}}{{.}}{{else}}!Join
            - ''
            - - {{with $c.ecr.account}}"{{.}}"{{else}}!Ref AWS::AccountId{{end}}
              - .dkr.ecr.{{$c.ecr.region}}.amazonaws.com/{{$c.ecr.repository}}:{{$c.ecr.tag}}{{end}}
          {{if $c.ports}}
          PortMappings:
          {{range $c.ports}}
          {{range $protocol, $port := .}}
            - ContainerPort: {{$port}}
              Protocol: {{$protocol}}{{end}}
          {{end}}
          {{end}}
          {{if $c.environment}}
          Environment:
          {{range $k, $v := $c.environment}}
            - Name: '{{$k}}'
              Value: '{{$v}}'{{end}}
          {{end}}
          Essential: {{$c.essential}}
          ReadonlyRootFilesystem: {{$c.readonly}}
          LogConfiguration:
            LogDriver: awslogs
            Options:
              awslogs-group: {{with $c.logs.group}}{{.}}{{else}}!Ref ServiceLogGroup{{end}}
              awslogs-stream-prefix: '{{with $c.logs.prefix}}{{.}}{{else}}{{$name}}{{end}}'
              awslogs-datetime-format: '{{$c.logs.datetime}}'
              awslogs-region: '{{$c.logs.region}}'
          {{if $c.ulimits}}
          Ulimits:
          {{range $k, $v := $c.ulimits}}
            - Name: {{$k}}
              SoftLimit: {{$v.soft_limit}}
              HardLimit: {{$v.hard_limit}}
          {{end}}
          {{end}}
          {{if $c.volumes_from}}
          VolumesFrom:
          {{range $c.volumes_from}}
            - SourceContainer: {{.container}}
              ReadOnly: {{with .readonly}}{{.}}{{else}}false{{end}}{{end}}
          {{end}}
          {{if $c.depends_on}}
          DependsOn:
          {{range $c.depends_on }}
            - ContainerName: {{.container}}
              Condition: {{with .condition}}{{.}}{{else}}START{{end}}
          {{end}}
          {{end}}
	  {{if $c.health_check.command}}
          HealthCheck:
            Command:
            {{range $c.health_check.command}}
              - {{.}}
            {{end}}
            Interval: {{$c.health_check.interval}}
            Retries: {{$c.health_check.retries}}
            StartPeriod: {{$c.health_check.start_period}}
            Timeout: {{$c.health_check.timeout}}
	  {{end}}
      {{end}}
      Cpu: {{$.aws.ecs.task.cpu}}
      ExecutionRoleArn: {{$.aws.ecs.task.execution_role}}
      Memory: {{$.aws.ecs.task.memory}}
      NetworkMode: awsvpc
      TaskRoleArn: {{if $.aws.iam.role_arn}}{{$.aws.iam.role_arn}}{{else}}!Ref Role{{end}}
  Service:
    Type: AWS::ECS::Service
    {{if not $.aws.iam.role_arn}}
    DependsOn:
      - Role
      {{range $k, $v := $.aws.network_load_balancers}}
      - {{logicalid $k "NLB"}}
      - {{logicalid $k "Listener"}}
      {{end}}
    {{end}}
    Properties:
      Cluster: {{$.aws.ecs.cluster}}
      DeploymentConfiguration:
        DeploymentCircuitBreaker:
          Enable: True
          Rollback: True
      DesiredCount: {{$.scaling.desired}}
      LaunchType: FARGATE
      PlatformVersion: {{$.aws.ecs.platform_version}}
      PropagateTags: TASK_DEFINITION
      TaskDefinition: !Ref TaskDefinition
      NetworkConfiguration:
        AwsvpcConfiguration:
          {{with $.aws.ecs.task.assign_public_ip}}
          AssignPublicIp: {{.}}
          {{end}}
          {{if or (or $.aws.ecs.task.ingress $.aws.ecs.task.egress) $.aws.ecs.task.security_groups}}
          SecurityGroups:
          {{if or $.aws.ecs.task.ingress $.aws.ecs.task.egress}}
            - !GetAtt TaskSG.GroupId
          {{end}}
          {{if $.aws.ecs.task.security_groups}}
          {{range $.aws.ecs.task.security_groups}}
            - '{{.}}'
          {{end}}
          {{end}}
          {{end}}
          Subnets:
          {{range $.aws.ecs.task.subnets}}
            - {{.}}
          {{end}}
      {{with $.aws.ecs.name}}ServiceName: {{.}}{{end}}
      {{if or $.aws.application_load_balancers $.aws.network_load_balancers}} 
      HealthCheckGracePeriodSeconds: {{$.aws.ecs.task.grace_period}}
      LoadBalancers: {{end}}
      {{range $k, $v := $.aws.application_load_balancers}}
        - TargetGroupArn: {{with $v.target_group}}{{.}}{{else}}!Ref {{logicalid $k "TargetGroup"}}{{end}}
          ContainerName: {{$v.container.name}}
          {{with $v.container.port}}ContainerPort: {{.}}{{end}}
      {{end}}
      {{range $k, $v := $.aws.network_load_balancers}}
        - TargetGroupArn: {{with $v.target_group}}{{.}}{{else}}!Ref {{logicalid $k "TargetGroup"}}{{end}}
          ContainerName: '{{$v.container.name}}'
          {{with $v.container.port}}ContainerPort: {{.}}{{end}}
      {{end}}
      {{if $.aws.service_discovery}}
      ServiceRegistries:
        - RegistryArn: {{with $.aws.service_discovery.cloudmap.service}}!Sub 'arn:aws:servicediscovery:${AWS::Region}:${AWS::AccountId}:service/{{.}}'{{else}}!GetAtt CloudMapService.Arn{{end}}
          ContainerName: {{$.aws.service_discovery.cloudmap.container}}
      {{end}}

{{if or $.aws.ecs.task.ingress $.aws.ecs.task.egress}}
  TaskSG:
    Type: AWS::EC2::SecurityGroup
    Properties:
      GroupDescription: Task SG for {{$.name}}
      VpcId: {{$.aws.vpc}}
      {{with $.aws.ecs.task.ingress}}
      SecurityGroupIngress:
      {{range $k, $v := .}}
      {{range $source := $v.allow_ingress_from}} {{/* TODO: add support for using security groups as source */}}
        - CidrIp{{if contains $source ":"}}v6{{end}}: {{$source}}
          Description: "{{$k}}: {{$v.description}}"
          {{with $v.ports}}
          FromPort: {{rangestart .}}
          ToPort: {{rangeend .}}
          {{else}}
          FromPort: {{$v.port}}
          ToPort: {{$v.port}}
          {{end}}
          IpProtocol: {{$v.protocol}}
      {{end}}
      {{end}}
      {{end}}
      {{with $.aws.ecs.task.egress}}
      SecurityGroupEgress:
      {{range $k, $v := .}}
      {{range $dest := $v.allow_egress_to}} {{/* TODO: add support for using security groups as destination */}}
        - CidrIp{{if contains $dest ":"}}v6{{end}}: {{$dest}}
          Description: "{{$k}}: {{$v.description}}"
          {{with $v.ports}}
          FromPort: {{rangestart .}}
          ToPort: {{rangeend .}}
          {{else}}
          FromPort: {{$v.port}}
          ToPort: {{$v.port}}
          {{end}}
          IpProtocol: {{$v.protocol}}
      {{end}}
      {{end}}
      {{end}}
{{end}}

{{if $.aws.service_discovery}}
{{if $.aws.service_discovery.cloudmap.namespace}}
  CloudMapService:
    Type: 'AWS::ServiceDiscovery::Service'
    Properties:
      DnsConfig:
        DnsRecords:
          - TTL: 10
            Type: "A"
        RoutingPolicy: MULTIVALUE
      HealthCheckCustomConfig:
        FailureThreshold: 1
      Name: "{{$.name}}"
      NamespaceId: "{{$.aws.service_discovery.cloudmap.namespace}}"
{{end}}
{{end}}

{{if not $.aws.iam.role_arn}}
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: 2012-10-17
        Statement:
          - Effect: Allow
            Principal:
              Service:
                - ecs-tasks.amazonaws.com
            Action: sts:AssumeRole
      Description: IAM role for {{$.name}}
      {{if $.aws.iam.role.managed_policies}}
      ManagedPolicyArns:
      {{range $.aws.iam.role.managed_policies}}
        - {{.}}
      {{end}}
      {{end}}
      {{with $.aws.iam.role.path}}Path: {{.}}{{end}}
      {{if $.aws.iam.role.policy_statements}}
      Policies:
      {{range $k, $v := $.aws.iam.role.policy_statements}}
        - PolicyName: {{$k}}
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: {{titlecase $v.effect}}
                Action:{{range $v.action}}
                  - "{{.}}"
                {{end}}
                Resource:{{range $v.resource}}
                  - "{{.}}"
                {{end}}
      {{end}}
      {{end}}

{{end}}

  AutoScalingTarget:
    Type: AWS::ApplicationAutoScaling::ScalableTarget
    DependsOn: Service
    Properties:
      MaxCapacity: {{$.scaling.max}}
      MinCapacity: {{$.scaling.min}}
      ResourceId: !Sub 'service/{{$.aws.ecs.cluster}}/${Service.Name}'
      ScalableDimension: ecs:service:DesiredCount
      ServiceNamespace: ecs
      RoleARN: !Sub 'arn:aws:iam::${AWS::AccountId}:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService'

{{range $k, $v := $.scaling.step_scaling}}
  {{logicalid $k "ScalingAlarm"}}:
    Type: AWS::CloudWatch::Alarm
    Properties:
      {{with $v.description}}AlarmDescription: {{.}}{{end}}
      ComparisonOperator: "{{$v.when.comparison}}"
      EvaluationPeriods: {{$v.times}}
      MetricName: "{{$v.when.metric}}"
      Namespace: "{{$v.when.namespace}}"
      Period: {{$v.period}}
      Statistic: "{{$v.when.statistic}}"
      Threshold: {{$v.when.threshold}}
      Dimensions:
      {{if eq $v.when.namespace "AWS/ECS" "ECS/ContainerInsights" }}
        - Name: ClusterName
          Value: '{{$.aws.ecs.cluster}}'
        - Name: ServiceName
          Value: !GetAtt Service.Name
      {{else}}
      {{range $dk, $dv := $v.when.dimensions}}
        - Name: {{$dk}}
          Value: {{$dv}}
      {{end}}
      {{end}}
      AlarmActions:
        - !Ref {{logicalid $k "ScalingPolicy"}}

  {{logicalid $k "ScalingPolicy"}}:
    Type: AWS::ApplicationAutoScaling::ScalingPolicy
    Properties:
      PolicyName: !Sub "${Service.Name}-{{$k}}"
      PolicyType: StepScaling
      ScalingTargetId: !Ref AutoScalingTarget
      StepScalingPolicyConfiguration:
        AdjustmentType: "{{$v.adjustment_type}}"
        Cooldown: {{$v.cooldown}}
        MetricAggregationType: "{{$v.when.statistic}}"
        StepAdjustments:
        {{if $v.adjustment}}
          - ScalingAdjustment: {{$v.adjustment}}
            {{if ge $v.adjustment 0}}
            MetricIntervalLowerBound: 0
            {{end}}
            {{if lt $v.adjustment 0}}
            MetricIntervalUpperBound: 0
            {{end}}
        {{end}}
{{end}}

{{range $k, $v := $.monitoring.cloudwatch.alarms}}
  {{logicalid $k "Alarm"}}:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmDescription: {{$v.description}}
      ComparisonOperator: "{{$v.when.comparison}}"
      EvaluationPeriods: {{$v.times}}
      MetricName: "{{$v.when.metric}}"
      Namespace: "{{$v.when.namespace}}"
      Period: {{$v.period}}
      Statistic: "{{$v.when.statistic}}"
      Threshold: {{$v.when.threshold}}
      TreatMissingData: {{$v.treat_missing_data}}
      Dimensions:
      {{if eq $v.when.namespace "AWS/ECS" "ECS/ContainerInsights" }}
        - Name: ClusterName
          Value: '{{$.aws.ecs.cluster}}'
        - Name: ServiceName
          Value: !GetAtt Service.Name
      {{else}}
      {{range $dk, $dv := $v.when.dimensions}}
        - Name: {{$dk}}
          Value: {{$dv}}
      {{end}}
      {{end}}
      {{if $v.notify}}
      {{range $v.notify_on}}
      {{if eq . "alarm"}}
      AlarmActions:
      {{range $v.notify}}
        - {{.}}
      {{end}}
      {{end}}
      {{if eq . "ok"}}
      OKActions:
      {{range $v.notify}}
        - {{.}}
      {{end}}
      {{end}}
      {{if eq . "insufficient_data"}}
      InsufficientDataActions:
      {{range $v.notify}}
        - {{.}}
      {{end}}
      {{end}}
      {{end}}
      {{end}}
{{end}}
{{range $k, $v := $.aws.application_load_balancers}}
{{if not $v.target_group}}
  {{logicalid $k "TargetGroup"}}:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties:
      HealthCheckEnabled: true
      {{with $v.health_check}}
      {{with .path}}
      HealthCheckPath: '{{.}}'{{end}}
      {{with .protocol}}
      HealthCheckProtocol: {{.}}{{end}}
      {{with .port}}
      HealthCheckPort: {{.}}{{end}}
      {{with .timeout}}
      HealthCheckTimeoutSeconds: {{.}}{{end}}
      {{with .interval}}
      HealthCheckIntervalSeconds: {{.}}{{end}}
      {{with .healthy_threshold}}
      HealthyThresholdCount: {{.}}{{end}}
      {{with .unhealthy_threshold}}
      UnhealthyThresholdCount: {{.}}{{end}}
      {{with $v.connection_draining_timeout}}
      TargetGroupAttributes:
        - Key: deregistration_delay.timeout_seconds
          Value: {{.}}{{end}}
      TargetType: ip
      {{with $v.protocol}}
      Protocol: {{.}}{{end}}
      {{end}}
      {{with $v.container.port}}
      Port: {{.}}{{end}}
      VpcId: {{$.aws.vpc}}
{{end}}
{{range $lk, $lv := $v.listener_rules}}
  {{logicalid $k $lk "ListenerRule"}}:
    Type: AWS::ElasticLoadBalancingV2::ListenerRule
    Properties:
      Actions:
        - TargetGroupArn: {{with $v.target_group}}{{.}}{{else}}!Ref {{logicalid $k "TargetGroup"}}{{end}}
          Type: forward
      Conditions:
      {{with $lv.path}}
        - Field: path-pattern
          PathPatternConfig:
            Values:
              - '{{.}}'{{end}}
      {{with $lv.hostname}}
        - Field: host-header
          HostHeaderConfig:
            Values:
              - '{{.}}'{{end}}
      ListenerArn: {{$lv.listener_arn}}
      Priority: {{with $lv.priority}}{{.}}{{else}}!GetAtt {{logicalid $k $lk "ListenerRulePriorityCalc"}}.priority

  {{logicalid $k $lk "ListenerRulePriorityCalc"}}:
    Type: Custom::ListenerRulePriorityCalc
    Properties:
      ServiceToken: {{$.yeet.priority_calculator_func_arn}}
      ListenerArn: {{$lv.listener_arn}}
      {{with $lv.path}}AlbPath: '{{.}}'{{end}}
      {{with $lv.hostname}}AlbHostname: '{{.}}'{{end}}
{{end}}
{{end}}
{{end}}

{{range $k, $v := $.aws.network_load_balancers}}
  {{if not $v.target_group}}
  {{logicalid $k "NLB"}}:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties:
      IpAddressType: ipv4
      LoadBalancerAttributes:
      {{if $v.access_logging.bucket}}
        - Key: access_logs.s3.enabled
          Value: true
        - Key: access_logs.s3.bucket
          Value: {{$v.access_logging.bucket}}
        {{if $v.access_logging.prefix}}
        - Key: access_logs.s3.prefix
          Value: {{$v.access_logging.prefix}}
        {{end}}
      {{end}}
      {{if $v.cross_zone}}
        - Key: load_balancing.cross_zone.enabled
          Value: {{$v.cross_zone}}
      {{end}}
      Scheme: {{$v.scheme}}
      Subnets:
      {{range $s := $v.subnets}}
        - {{$s}}
      {{end}}
      Type: network

  {{logicalid $k "TargetGroup"}}:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties:
      HealthCheckEnabled: true
      {{with $v.health_check}}
      {{with .path}}
      HealthCheckPath: '{{.}}'{{end}}
      {{with .protocol}}
      HealthCheckProtocol: {{.}}{{end}}
      {{with .port}}
      HealthCheckPort: {{.}}{{end}}
      {{with .timeout}}
      HealthCheckTimeoutSeconds: {{.}}{{end}}
      {{with .interval}}
      HealthCheckIntervalSeconds: {{.}}{{end}}
      {{with .healthy_threshold}}
      HealthyThresholdCount: {{.}}{{end}}
      {{with .unhealthy_threshold}}
      UnhealthyThresholdCount: {{.}}{{end}}
      {{end}}
      {{with $v.protocol}}
      Protocol: {{.}}{{end}}
      {{with $v.container.port}}
      Port: {{.}}{{end}}
      TargetGroupAttributes:
        {{with $v.connection_draining_timeout}}
        - Key: deregistration_delay.timeout_seconds
          Value: {{.}}{{end}}
        {{with $v.stickiness}}
        - Key: stickiness.enabled
          Value: true
        - Key: stickiness.type
          Value: {{.}}{{end}}
        {{with $v.proxy_protocol_v2}}
        - Key: proxy_protocol_v2.enabled
          Value: {{.}}{{end}}
      TargetType: ip
      VpcId: {{$.aws.vpc}}

  {{logicalid $k "Listener"}}:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      DefaultActions:
        - Type: forward
          TargetGroupArn: !Ref {{logicalid $k "TargetGroup"}}
      LoadBalancerArn: !Ref {{logicalid $k "NLB"}}
      Port: {{$v.port}}
      Protocol: {{$v.protocol}}

  {{range $dk, $dv := $v.dns}}
  {{logicalid $dk $k "DNS"}}:
    Type: AWS::Route53::RecordSet
    Properties:
      AliasTarget:
        DNSName: !GetAtt {{logicalid $k "NLB"}}.DNSName
        HostedZoneId: !GetAtt {{logicalid $k "NLB"}}.CanonicalHostedZoneID
      {{with $dv.zone}}HostedZoneName: {{.}}
      {{else}}HostedZoneId: {{$dv.zone_id}}{{end}}
      Name: {{$dk}}
      SetIdentifier: "{{$.name}} {{$k}} {{$dk}}"
      Type: A
      Weight: {{$dv.weight}}
  {{end}}
  {{end}}
{{end}}

{{with $.yeet.timeout_func_arn}}
  {{logicalid "Timeout" $r}}:
    Type: Custom::Timeout
    DependsOn: Service
    Properties:
      ServiceToken: '{{.}}'
      StackId: !Ref AWS::StackId
      TaskDefinitionVersion: !Ref TaskDefinition
      WaitCondition: {{logicalid "WaitCondition" $r}}

  {{logicalid "WaitCondition" $r}}:
    Type: AWS::CloudFormation::WaitCondition
    CreationPolicy:
      ResourceSignal:
        Count: 1
        Timeout: {{$.aws.ecs.deployment.timeout}}
{{end}}

{{$loggroupcreated := false}}
{{range $k, $v := $.containers}}
{{if not $v.logs.group}}
{{if not $loggroupcreated}}
{{$loggroupcreated = true}}
  ServiceLogGroup:
    Type: AWS::Logs::LogGroup
    DeletionPolicy: Retain
    UpdateReplacePolicy: Retain
    {{with $.monitoring.logs.retention}}
    Properties:
      RetentionInDays: {{.}}{{end}}

{{with $.monitoring.logs.s3}}
  ServiceLogsSubscriptionFilter:
    Type: 'AWS::Logs::SubscriptionFilter'
    Properties:
      DestinationArn: !GetAtt ServiceLogsDeliveryStream.Arn
      FilterPattern: ""
      LogGroupName: !Ref ServiceLogGroup
      RoleArn: !GetAtt ServiceLogsSubscriptionRole.Arn

  ServiceLogsSubscriptionRole:
    Type: 'AWS::IAM::Role'
    Properties:
      AssumeRolePolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: Allow
            Principal:
              Service:
                - !Sub "logs.${AWS::Region}.amazonaws.com"
            Action:
              - "sts:AssumeRole"
      Path: /
      Policies:
        - PolicyName: CloudWatchLogs_Firehose
          PolicyDocument:
            Version: "2012-10-17"
            Statement:
              - Effect: Allow
                Action:
                  - firehose:PutRecord
                Resource: !GetAtt ServiceLogsDeliveryStream.Arn

  ServiceLogsDeliveryStream:
    Type: AWS::KinesisFirehose::DeliveryStream
    Properties:
      S3DestinationConfiguration:
        BucketARN: '{{.bucket}}'
        CompressionFormat: GZIP
        Prefix: '{{.prefix}}/'
        {{with .kms}}
        EncryptionConfiguration:
          KMSEncryptionConfig:
            AWSKMSKeyARN: '{{.}}'{{end}}
        {{with .role}}
        RoleARN: '{{.}}'{{end}}
{{end}}
{{end}}
{{end}}
{{end}}

Outputs:
  Cluster:
    Description: ARN for the ECS Cluster where the ECS Service is deployed
    Value: '{{$.aws.ecs.cluster}}'

  Service:
    Description: ARN for the ECS Service
    Value: !Ref Service

  RandomValue:
    Description: Random value used by Yeet
    Value: {{$r}}
