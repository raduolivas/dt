locals {
  services = ["gateway", "deliveries", "tracking", "webhooks", "accounts"]
}

# Repositories dt-<svc> (the deploy role may push to dt-* only). Tags are
# immutable: a deploy pushes <commit sha> once, and skips the build when the
# tag exists (a re-run), so the cosign signature of a digest is written once.
# trivy:ignore:AVD-AWS-0033 Images hold no secrets; AES256 (S3-managed) is enough for staging and keeps KMS calls off every pull.
resource "aws_ecr_repository" "svc" {
  for_each = toset(local.services)

  name                 = "dt-${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = true # staging-down removes the images too

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "svc" {
  for_each = aws_ecr_repository.svc

  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the last 15 images (rollbacks need only a few)"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 15
      }
      action = { type = "expire" }
    }]
  })
}
