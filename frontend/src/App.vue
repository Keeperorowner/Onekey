<template>
  <n-config-provider :theme="theme" :theme-overrides="themeOverrides">
    <n-message-provider>
      <n-dialog-provider>
        <!-- OOBE: full-screen, no sidebar -->
        <template v-if="route.name === 'oobe'">
          <div class="titlebar" style="--wails-draggable: drag;">
            <div class="titlebar-drag"></div>
            <div class="titlebar-controls">
              <button class="titlebar-btn" @click="WindowMinimise">&#x2013;</button>
              <button class="titlebar-btn" @click="WindowToggleMaximise">&#9633;</button>
              <button class="titlebar-btn titlebar-close" @click="Quit">&#10005;</button>
            </div>
          </div>
          <router-view style="height: calc(100vh - 32px);"/>
        </template>

        <!-- Main layout with sidebar -->
        <template v-else>
          <n-layout has-sider style="height: 100vh">
            <n-layout-sider
                :collapsed="collapsed"
                :collapsed-width="64"
                :width="220"
                bordered
                collapse-mode="width"
                show-trigger
                @collapse="collapsed = true"
                @expand="collapsed = false"
            >
              <div class="sider-content">
                <div class="sider-top">
                  <div v-if="!collapsed" class="sider-logo">
                    <n-text strong style="font-size: 18px;">Onekey</n-text>
                  </div>
                  <n-menu
                      :collapsed="collapsed"
                      :collapsed-icon-size="22"
                      :collapsed-width="64"
                      :options="menuOptions"
                      :value="activeKey"
                      @update:value="handleMenuSelect"
                  />
                </div>
                <div class="sider-actions">
                  <n-divider style="margin: 8px 0"/>
                  <n-button block quaternary size="small" @click="showLogDrawer = true">
                    <template #icon>
                      <n-icon :component="DocumentTextOutline"/>
                    </template>
                    <span v-if="!collapsed">{{ t('sidebar.task_log') }}</span>
                  </n-button>
                  <n-button block quaternary size="small" @click="toggleTheme">
                    <template #icon>
                      <n-icon :component="isDark ? SunnyOutline : MoonOutline"/>
                    </template>
                    <span v-if="!collapsed">{{ isDark ? t('sidebar.light_mode') : t('sidebar.dark_mode') }}</span>
                  </n-button>
                </div>
              </div>
            </n-layout-sider>

            <n-layout>
              <!-- Title bar -->
              <div class="titlebar" style="--wails-draggable: drag;">
                <div class="titlebar-drag"></div>
                <div class="titlebar-controls">
                  <button class="titlebar-btn" @click="WindowMinimise">&#x2013;</button>
                  <button class="titlebar-btn" @click="WindowToggleMaximise">&#9633;</button>
                  <button class="titlebar-btn titlebar-close" @click="Quit">&#10005;</button>
                </div>
              </div>
              <n-layout-content :native-scrollbar="false" content-style="padding: 24px;"
                                style="height: calc(100vh - 32px);">
                <router-view/>
              </n-layout-content>
            </n-layout>
          </n-layout>
        </template>
        <!-- Task Log Drawer -->
        <n-drawer v-model:show="showLogDrawer" :width="460" placement="right">
          <n-drawer-content :title="t('sidebar.task_log')" closable>
            <n-space :size="8" align="center" style="margin-bottom: 12px;">
              <n-tag v-if="store.taskStatus === 'running'" type="info" size="small" round>{{ t('home.task_running') }}</n-tag>
              <n-tag v-else-if="store.taskStatus === 'completed'" type="success" size="small" round>{{ t('sidebar.log_completed') }}</n-tag>
              <n-tag v-else-if="store.taskStatus === 'error'" type="error" size="small" round>{{ t('sidebar.log_error') }}</n-tag>
              <n-tag v-else type="default" size="small" round>{{ t('sidebar.log_idle') }}</n-tag>
              <n-button size="tiny" quaternary @click="store.clearLogs()">{{ t('sidebar.log_clear') }}</n-button>
            </n-space>
            <n-timeline>
              <n-timeline-item
                  v-for="(log, i) in store.logs"
                  :key="i"
                  :time="log.timestamp"
                  :type="log.type === 'error' ? 'error' : log.type === 'warning' ? 'warning' : 'info'"
              >
                <span :style="{color: log.type === 'error' ? '#d03050' : log.type === 'warning' ? '#f0a020' : 'inherit'}">{{ log.message }}</span>
              </n-timeline-item>
            </n-timeline>
            <n-empty v-if="store.logs.length === 0" :description="t('home.log_placeholder')"/>
          </n-drawer-content>
        </n-drawer>
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<script lang="ts" setup>
import {computed, h, onMounted, ref} from 'vue'
import {useRoute, useRouter} from 'vue-router'
import {createDiscreteApi, darkTheme, type GlobalThemeOverrides, type MenuOption, NIcon} from 'naive-ui'
import {
  GameControllerOutline,
  HomeOutline,
  SettingsOutline,
  SunnyOutline,
  MoonOutline,
  DocumentTextOutline,
} from '@vicons/ionicons5'
import {useI18n} from './i18n'
import {useAppStore} from './stores/app'
import {
  GetDetailedConfig,
} from '../wailsjs/go/main/App'
import {EventsOn, Quit, WindowMinimise, WindowToggleMaximise} from '../wailsjs/runtime/runtime'

const {t} = useI18n()
const route = useRoute()
const router = useRouter()
const store = useAppStore()

// Theme
const isDark = ref(localStorage.getItem('theme') === 'dark')
const theme = computed(() => isDark.value ? darkTheme : null)
const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#6750a4',
    primaryColorHover: '#7c6bb5',
    primaryColorPressed: '#5a3f96',
    fontFamily: '"LXGW WenKai Mono", sans-serif',
  },
}

// Discrete API for message/dialog — App.vue renders the providers itself,
// so useMessage()/useDialog() can't inject from them. createDiscreteApi
// creates standalone instances that work anywhere.
const {message, dialog} = createDiscreteApi(
    ['message', 'dialog'],
    {
      configProviderProps: computed(() => ({
        theme: isDark.value ? darkTheme : undefined,
        themeOverrides,
      })),
    }
)

function toggleTheme() {
  isDark.value = !isDark.value
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Sidebar collapse
const collapsed = ref(false)
const showLogDrawer = ref(false)

// Menu
function renderIcon(icon: any) {
  return () => h(NIcon, null, {default: () => h(icon)})
}

const menuOptions: MenuOption[] = [
  {label: () => t('nav.home'), key: 'home', icon: renderIcon(HomeOutline)},
  {label: () => t('nav.games'), key: 'games', icon: renderIcon(GameControllerOutline)},
  {label: () => t('nav.settings'), key: 'settings', icon: renderIcon(SettingsOutline)},
]

const activeKey = computed(() => {
  const name = route.name as string
  return name || 'home'
})

function handleMenuSelect(key: string) {
  if (key === 'home') router.push('/')
  else router.push(`/${key}`)
}

// Sidebar actions

onMounted(async () => {
  // OOBE check: redirect to setup if no key configured
  if (route.name !== 'oobe') {
    try {
      const cfg = await GetDetailedConfig()
      if (cfg.success && (!cfg.config.key || cfg.config.key === '')) {
        router.push('/oobe')
        return
      }
    } catch (e) {
      router.push('/oobe')
      return
    }
  }

  // Global task event listeners — persist across route changes
  EventsOn('task_progress', (data: any) => {
    store.addLog(data.type, data.message)
  })
  EventsOn('task_done', (result: any) => {
    if (result && result.success) {
      store.setTaskStatus('completed')
      message.success(result.message)
    } else if (result) {
      store.setTaskStatus('error')
      message.error(result.message)
    }
    store.setTaskStatus('idle')
  })
})
</script>

<style>
body {
  margin: 0;
  padding: 0;
  font-family: 'LXGW WenKai Mono', sans-serif;
  overflow: hidden;
}

.md-content h1, .md-content h2, .md-content h3,
.md-content h4, .md-content h5, .md-content h6 {
  margin: 8px 0 4px;
}

.md-content h1 {
  font-size: 1.4em;
}

.md-content h2 {
  font-size: 1.2em;
}

.md-content h3 {
  font-size: 1.1em;
}

.md-content p {
  margin: 4px 0;
}

.md-content ul, .md-content ol {
  padding-left: 20px;
  margin: 4px 0;
}

.md-content code {
  background: rgba(128, 128, 128, 0.15);
  padding: 1px 4px;
  border-radius: 3px;
  font-size: 0.9em;
}

.md-content pre {
  background: rgba(128, 128, 128, 0.1);
  padding: 8px 12px;
  border-radius: 6px;
  overflow-x: auto;
}

.md-content pre code {
  background: none;
  padding: 0;
}

.md-content blockquote {
  border-left: 3px solid #6750a4;
  margin: 4px 0;
  padding: 4px 12px;
  opacity: 0.85;
}

.md-content a {
  color: #6750a4;
}

.md-content img {
  max-width: 100%;
  border-radius: 6px;
}
</style>

<style scoped>
.titlebar {
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  user-select: none;
  flex-shrink: 0;
}

.titlebar-drag {
  flex: 1;
  height: 100%;
}

.titlebar-controls {
  display: flex;
  height: 100%;
  --wails-draggable: none;
}

.titlebar-btn {
  width: 46px;
  height: 100%;
  border: none;
  background: transparent;
  color: inherit;
  font-size: 13px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background 0.15s;
}

.titlebar-btn:hover {
  background: rgba(128, 128, 128, 0.2);
}

.titlebar-close:hover {
  background: #e81123;
  color: white;
}

.sider-content {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.sider-top {
  flex: 1;
}

.sider-logo {
  padding: 16px 20px 8px;
}

.sider-actions {
  padding: 4px 8px 12px;
}
</style>
