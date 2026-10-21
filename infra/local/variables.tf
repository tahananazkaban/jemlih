variable "aws_endpoint" {
  description = "Endpoint of the local AWS emulator."
  type        = string
  default     = "http://localhost:4566"
}

variable "fleet_table" {
  description = "Name of the fleet DynamoDB table; must match FLEET_TABLE of the service."
  type        = string
  default     = "jemlih-fleet"
}

variable "region" {
  description = "AWS region used by every resource."
  type        = string
  default     = "us-east-1"
}
