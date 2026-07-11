# CloudFormation custom resource example

This example deploys a Voker-backed CloudFormation custom resource in
`us-west-2`. The handler echoes typed properties through `Fn::GetAtt`, keeps a
stable physical resource ID across lifecycle operations, and logs each native
CloudFormation request and handler result with `VOKER_CFN_EVENT` and
`VOKER_CFN_RESULT` markers.

```sh
make deploy
```

Change `Message` to exercise an Update request:

```sh
aws cloudformation deploy \
  --region us-west-2 \
  --stack-name voker-cloudformation-example \
  --template-file /tmp/voker-cloudformation-example-packaged.yml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides 'Message=updated from CloudFormation' Count=11
```

Delete the example and its artifact bucket when finished:

```sh
make delete
```
