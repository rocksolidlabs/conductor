<script setup lang="ts">
/** Open a server as a screen of its own: from the sign-in screen and from Settings, in every state the provider reports. */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Open a server' })
const shell = useShell()
const route = useRoute()
const initial = computed(() => (typeof route.query.address === 'string' ? route.query.address : ''))
</script>

<template>
  <ShellHeader title="Open a server" line="Its own pages open in the in-app browser." :back="!!shell.provider" />
  <ShellOpenServer v-if="shell.bridge" :bridge="shell.bridge" :initial="initial" />
</template>
