<script setup lang="ts">
/** The sign-in screen at its own address; once the provider reports this phone signed in, the shell's first screen. */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Sign in' })
const shell = useShell()
watch(
  () => shell.state.value?.signedIn,
  (inNow) => {
    if (inNow || !shell.provider) void navigateTo('/shell', { replace: true })
  },
  { immediate: true },
)
</script>

<template>
  <ShellSignIn v-if="shell.provider" />
</template>
