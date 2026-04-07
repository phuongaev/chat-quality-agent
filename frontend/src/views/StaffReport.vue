<template>
  <div>
    <h1 class="text-h5 font-weight-bold mb-4">
      <v-icon class="mr-2">mdi-headset</v-icon>
      Báo cáo nhân viên
    </h1>

    <!-- Filters -->
    <v-card class="mb-4" variant="outlined">
      <v-card-text class="pa-3">
        <v-row dense align="center">
          <v-col cols="6" sm="3">
            <v-text-field
              v-model="filterFrom"
              label="Từ ngày"
              type="date"
              density="compact"
              variant="outlined"
              hide-details
            />
          </v-col>
          <v-col cols="6" sm="3">
            <v-text-field
              v-model="filterTo"
              label="Đến ngày"
              type="date"
              density="compact"
              variant="outlined"
              hide-details
            />
          </v-col>
          <v-col cols="6" sm="3">
            <v-select
              v-model="filterChannelId"
              :items="channelOptions"
              label="Kênh chat"
              clearable
              density="compact"
              variant="outlined"
              hide-details
            />
          </v-col>
          <v-col cols="6" sm="3">
            <v-btn color="primary" :loading="loading" @click="loadReport" block>
              <v-icon start>mdi-magnify</v-icon> Xem báo cáo
            </v-btn>
          </v-col>
        </v-row>
      </v-card-text>
    </v-card>

    <!-- Loading -->
    <v-card v-if="loading" class="text-center py-8" variant="outlined">
      <v-progress-circular indeterminate size="32" />
      <div class="text-grey mt-2">Đang tải dữ liệu...</div>
    </v-card>

    <!-- Empty state -->
    <v-card v-else-if="!loading && loaded && staff.length === 0" class="text-center py-8" variant="outlined">
      <v-icon size="48" color="grey-lighten-1">mdi-account-off</v-icon>
      <div class="text-grey-darken-1 mt-2">Không có dữ liệu nhân viên trong khoảng thời gian này.</div>
      <div class="text-caption text-grey">Hãy đảm bảo đã đồng bộ tin nhắn và chạy đánh giá AI.</div>
    </v-card>

    <!-- Staff Table -->
    <v-card v-else-if="staff.length > 0" variant="outlined">
      <v-table density="comfortable">
        <thead>
          <tr>
            <th>#</th>
            <th>Nhân viên</th>
            <th class="text-center">Hội thoại</th>
            <th class="text-center">Tin nhắn</th>
            <th class="text-center">Đã đánh giá</th>
            <th class="text-center">Đạt</th>
            <th class="text-center">Không đạt</th>
            <th class="text-center">Tỷ lệ đạt</th>
            <th class="text-center">Vi phạm</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(s, i) in sortedStaff" :key="s.name" @click="selectStaff(s)" style="cursor: pointer">
            <td>{{ i + 1 }}</td>
            <td>
              <div class="d-flex align-center">
                <v-avatar :color="rankColor(i)" size="28" class="mr-2">
                  <span class="text-caption font-weight-bold white--text">{{ s.name.charAt(0).toUpperCase() }}</span>
                </v-avatar>
                <div>
                  <div class="font-weight-medium">{{ s.name }}</div>
                  <div v-if="s.sender_external_id" class="text-caption text-grey">ID: {{ s.sender_external_id }}</div>
                </div>
              </div>
            </td>
            <td class="text-center">{{ s.total_conversations }}</td>
            <td class="text-center">{{ s.total_messages }}</td>
            <td class="text-center">{{ s.evaluated_conversations }}</td>
            <td class="text-center">
              <v-chip v-if="s.pass_count > 0" size="small" color="success" variant="tonal">{{ s.pass_count }}</v-chip>
              <span v-else class="text-grey">0</span>
            </td>
            <td class="text-center">
              <v-chip v-if="s.fail_count > 0" size="small" color="error" variant="tonal">{{ s.fail_count }}</v-chip>
              <span v-else class="text-grey">0</span>
            </td>
            <td class="text-center">
              <v-chip
                v-if="s.evaluated_conversations > 0"
                size="small"
                :color="s.pass_rate >= 80 ? 'success' : s.pass_rate >= 50 ? 'warning' : 'error'"
                variant="tonal"
              >
                {{ s.pass_rate.toFixed(1) }}%
              </v-chip>
              <span v-else class="text-grey">—</span>
            </td>
            <td class="text-center">
              <template v-if="totalViolations(s) > 0">
                <v-chip v-if="s.violations['NGHIEM_TRONG']" size="x-small" color="error" variant="tonal" class="mr-1">
                  {{ s.violations['NGHIEM_TRONG'] }} nghiêm trọng
                </v-chip>
                <v-chip v-if="s.violations['CAN_CAI_THIEN']" size="x-small" color="warning" variant="tonal">
                  {{ s.violations['CAN_CAI_THIEN'] }} cần cải thiện
                </v-chip>
              </template>
              <span v-else class="text-grey">0</span>
            </td>
          </tr>
        </tbody>
      </v-table>
    </v-card>

    <!-- Summary Cards -->
    <v-row v-if="staff.length > 0" class="mt-4">
      <v-col cols="6" sm="3">
        <v-card variant="outlined" class="pa-4 text-center">
          <div class="text-h5 font-weight-bold">{{ staff.length }}</div>
          <div class="text-caption text-grey">Nhân viên</div>
        </v-card>
      </v-col>
      <v-col cols="6" sm="3">
        <v-card variant="outlined" class="pa-4 text-center">
          <div class="text-h5 font-weight-bold">{{ totalConversations }}</div>
          <div class="text-caption text-grey">Tổng hội thoại</div>
        </v-card>
      </v-col>
      <v-col cols="6" sm="3">
        <v-card variant="outlined" class="pa-4 text-center">
          <div class="text-h5 font-weight-bold" :class="avgPassRate >= 80 ? 'text-success' : avgPassRate >= 50 ? 'text-warning' : 'text-error'">
            {{ avgPassRate.toFixed(1) }}%
          </div>
          <div class="text-caption text-grey">Tỷ lệ đạt TB</div>
        </v-card>
      </v-col>
      <v-col cols="6" sm="3">
        <v-card variant="outlined" class="pa-4 text-center">
          <div class="text-h5 font-weight-bold text-error">{{ totalViolationsAll }}</div>
          <div class="text-caption text-grey">Tổng vi phạm</div>
        </v-card>
      </v-col>
    </v-row>

    <!-- Snackbar -->
    <v-snackbar v-model="snackbar" :color="snackColor" timeout="3000">{{ snackText }}</v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useChannelStore } from '../stores/channels'
import api from '../api'

interface StaffItem {
  name: string
  sender_external_id: string
  total_conversations: number
  total_messages: number
  evaluated_conversations: number
  pass_count: number
  fail_count: number
  pass_rate: number
  violations: Record<string, number>
}

const route = useRoute()
const router = useRouter()
const channelStore = useChannelStore()
const tenantId = computed(() => route.params.tenantId as string)

const loading = ref(false)
const loaded = ref(false)
const staff = ref<StaffItem[]>([])
const snackbar = ref(false)
const snackText = ref('')
const snackColor = ref('success')

const filterFrom = ref(new Date(Date.now() - 30 * 86400000).toISOString().slice(0, 10))
const filterTo = ref(new Date().toISOString().slice(0, 10))
const filterChannelId = ref<string | null>(null)

const channelOptions = computed(() =>
  channelStore.channels.map(c => ({ title: c.name, value: c.id }))
)

const sortedStaff = computed(() =>
  [...staff.value].sort((a, b) => {
    // Sort by pass_rate desc, then by total_conversations desc
    if (a.evaluated_conversations > 0 && b.evaluated_conversations > 0) {
      if (b.pass_rate !== a.pass_rate) return b.pass_rate - a.pass_rate
    }
    return b.total_conversations - a.total_conversations
  })
)

const totalConversations = computed(() =>
  staff.value.reduce((sum, s) => sum + s.total_conversations, 0)
)

const avgPassRate = computed(() => {
  const evaluated = staff.value.filter(s => s.evaluated_conversations > 0)
  if (evaluated.length === 0) return 0
  return evaluated.reduce((sum, s) => sum + s.pass_rate, 0) / evaluated.length
})

const totalViolationsAll = computed(() =>
  staff.value.reduce((sum, s) => sum + totalViolations(s), 0)
)

function totalViolations(s: StaffItem) {
  return Object.values(s.violations || {}).reduce((sum, v) => sum + v, 0)
}

function rankColor(index: number) {
  if (index === 0) return 'amber-darken-1'
  if (index === 1) return 'grey-darken-1'
  if (index === 2) return 'brown'
  return 'blue-grey'
}

function selectStaff(s: StaffItem) {
  // Navigate to messages filtered by this agent
  router.push({
    path: `/${tenantId.value}/messages`,
    query: { agent_name: s.name },
  })
}

async function loadReport() {
  loading.value = true
  try {
    const params: Record<string, string> = {
      from: filterFrom.value,
      to: filterTo.value,
    }
    if (filterChannelId.value) params.channel_id = filterChannelId.value

    const { data } = await api.get(`/tenants/${tenantId.value}/staff-report`, { params })
    staff.value = data.staff || []
    loaded.value = true
  } catch (e: any) {
    snackText.value = e?.response?.data?.error || 'Lỗi tải báo cáo'
    snackColor.value = 'error'
    snackbar.value = true
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  channelStore.fetchChannels(tenantId.value)
  loadReport()
})
</script>
