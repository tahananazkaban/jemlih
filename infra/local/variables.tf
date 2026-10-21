variable "aws_endpoint" {
  description = "Endpoint of the local AWS emulator, as seen from the host."
  type        = string
  default     = "http://localhost:4566"
}

variable "lambda_aws_endpoint" {
  description = "Endpoint of the local AWS emulator, as seen from Lambda containers."
  type        = string
  default     = "http://floci:4566"
}

variable "region" {
  description = "AWS region used by every resource."
  type        = string
  default     = "us-east-1"
}

variable "rental_id" {
  description = "The single Rental that self-registered Renters join."
  type        = string
  default     = "default"
}

variable "fleet_table" {
  description = "Name of the fleet DynamoDB table; must match FLEET_TABLE of the service."
  type        = string
  default     = "jemlih-fleet"
}

variable "account_post_confirmation_zip" {
  description = "Path of the built post-confirmation Lambda package."
  type        = string
  default     = "../../build/account-postconfirm.zip"
}
