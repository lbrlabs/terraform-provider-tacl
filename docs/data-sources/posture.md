---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "tacl_posture Data Source - terraform-provider-tacl"
subcategory: ""
description: |-
  Data source for reading a posture (named or default). If 'id' is 'default', we read /postures/default. Otherwise, /postures/:name.
---

# tacl_posture (Data Source)

Data source for reading a posture (named or default). If 'id' is 'default', we read /postures/default. Otherwise, /postures/:name.



<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `id` (String) Either a named posture (e.g. 'latestMac') or 'default'.

### Read-Only

- `rules` (List of String) Rules for this posture (strings).
