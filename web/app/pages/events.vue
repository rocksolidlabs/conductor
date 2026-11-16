<script setup lang="ts">
import { eventsRedirect } from '~/utils/events'

// The Events page is retired (the UX review's P12): an old link lands where its tab lives now, the feed in History, the rules in the
// Alerts panel over that feed, the agents' reporting in Agents (eventsRedirect).
definePageMeta({ redirect: (to) => eventsRedirect(to.query) })
</script>

<template>
  <div />
</template>
