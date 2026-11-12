# Deploy on a single EC2 VM with CloudFormation

`cfn.yaml` stands up one Ubuntu 24.04 EC2 instance (Docker installed by cloud-init), an
Elastic IP, a Route 53 A record and an imported SSH key pair, to run the Docker Compose
deployment (`deploy/compose`) on it.

| Page | What it covers |
|---|---|
| [Deploy on a single EC2 instance](../../../guide/operate/aws-ec2-single.md) | The stack's parameters, shipping the release bundle, starting it, instance metadata (IMDS), tearing it down |
| [Deploy with Docker Compose](../../../guide/operate/compose/install.md) | What `.env` holds on the instance |
