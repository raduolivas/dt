# Security Policy

Security is DT's first priority. DT handles live courier locations and customer delivery data, so we take
every report seriously.

## Reporting a vulnerability

Please report vulnerabilities **privately**. Do not open a public issue, pull request or discussion.

- **Preferred:** GitHub private vulnerability reporting on
  [`raduolivas/dt`](https://github.com/raduolivas/dt/security/advisories/new).
- **Email:** `security@<dt-domain>` (placeholder until the domain is chosen, see open question OQ14).

Include what you found, the steps to reproduce it, the impact you expect, and any proof-of-concept. Use test
data only; never access, change or keep real partner, courier or customer data.

## Scope

In scope:

- this repository: the backend services, shared libraries, contracts, web packages, local stack and CI
  configuration;
- once they exist, DT's hosted API, tracking pages, courier link pages, partner console and SDKs.

Out of scope: denial-of-service and volumetric testing, social engineering, physical attacks, and findings in
third-party services DT uses (report those to the vendor).

## What to expect

- We acknowledge your report within **3 business days**.
- We keep you informed while we investigate and fix it.
- We ask that you do not disclose the issue publicly until a fix is released. We will agree on a
  disclosure date with you and credit you if you wish.
