mailcheck
=========

A small daemon for verifying that mails are getting through

Emails are sent from a cron job on ScraperWiki boxes on various servers to an
email address. This program polls the emails and displays statistics about
delayed and missing emails which can be used for monitoring.

The boxes currently being used for this purpose are:

* d4zgz2a - for our free server
* dmpky2q - for our data services server
* enkhlgy - for our premium server

No emails should be sent from the free server so monitoring can look for any
emails that come from that server as a failure condition.

# How it works

mailcheck@scraperwiki.com receives e-mails from the various boxes. For each
host mails are considered in the order they were sent by the box. The (sent
time) delta between two received e-mails should be approximately equal to the
period at which the boxes are sending e-mails. A gap considerably larger than
the sending period shows that an email is lost or late. If an email has been
missing for more than 24 hours this is counted as a failure and the summary
shown for the server will say that the state is BAD.

If you wish to modify the "BAD" criterion, take a look [here](https://github.com/scraperwiki/mailcheck/blob/c378d8245b074704085a734d9ce9c2aeaddde1d5/main.go#L157) (note: this is pinned to a specific revision).

---

Deployment on AWS with juju:

```
juju deploy --to 44 --repository ~/sw/charms --config ~/sw/charms-secrets/config/dev/mailcheck.yaml local:raring/mailcheck mailcheck-dev

juju deploy --to 45 --repository ~/sw/charms --config ~/sw/charms-secrets/config/live/mailcheck.yaml local:raring/mailcheck mailcheck
```

When you make modifications to this repository run the following to make Juju download and install the changes:

```
juju upgrade-charm --repository ~/sw/charms mailcheck-dev

juju upgrade-charm --repository ~/sw/charms mailcheck
```

If you introduce a new config variable run the following command after upgrading the charm:

```
juju set --config ~/sw/charms-secrets/config/dev/mailcheck.yaml mailcheck-dev

juju set --config ~/sw/charms-secrets/config/live/mailcheck.yaml mailcheck
```
