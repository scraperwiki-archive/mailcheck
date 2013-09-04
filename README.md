mailcheck
=========

A small daemon for verifying that mails are getting through

Deployment on AWS with juju:

```
juju deploy --to 44 --repository ~/sw/charms --config ~/sw/charms-secrets/config/dev/mailcheck.yaml local:raring/mailcheck mailcheck-dev
```
