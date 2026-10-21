# Points every AWS client at the local emulator with throwaway credentials.
provider "aws" {
  region     = var.region
  access_key = "test"
  secret_key = "test"

  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    cognitoidp = var.aws_endpoint
    dynamodb   = var.aws_endpoint
    iam        = var.aws_endpoint
    lambda     = var.aws_endpoint
    sts        = var.aws_endpoint
  }

  default_tags {
    tags = {
      project     = "jemlih"
      environment = "local"
    }
  }
}
