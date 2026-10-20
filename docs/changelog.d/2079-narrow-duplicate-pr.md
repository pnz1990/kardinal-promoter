---
section: Fixed
---

- **A refused PR create is taken for a duplicate only on the SCM's own answer, and the PR it names is adopted only when it is kardinal's** (#2079). GitLab, Forgejo/Gitea and Azure DevOps took any error that contained "409" or "already exists" for a duplicate PR, a repository named `app409` included. The step then failed with "no open pull request" instead of the SCM's real reason. Each provider now matches only its own duplicate status and message ([SCM providers](scm-providers.md)), and any other refusal keeps its own message. The PR a refused duplicate names, which someone opened between kardinal's lookup and its create, is now adopted only on the same rule as one the lookup finds: its head is, or contains, the commit kardinal pushed. Without a recorded pushed commit there is nothing to compare its head with, and it is not adopted.
