# Security policy

## Supported versions

Security fixes target the latest release. Upgrade before reporting a problem that an existing release fixes. Development builds and older releases have no separate security maintenance branch.

## Report a vulnerability privately

Use [GitHub private vulnerability reporting](https://github.com/Dematom1/dibs/security/advisories/new). Do not open a public issue or pull request for a vulnerability.

Include the affected version, operating system, prerequisites, impact, and a minimal reproduction using disposable resources. Remove SSH keys, tokens, kubeconfigs, private hostnames, and production data. Do not attach live credentials.

Coordinate disclosure with the maintainer in the private report. No response deadline or bounty is promised.

## Trust boundaries

Dibs runs commands and manages resources with your local user's permissions. Configured SSH machines and adapter executables must be trusted. Do not run commands supplied by untrusted issues or pull requests against production machines, containers, or services. Use isolated test resources and temporary state directories.
