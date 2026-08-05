/**
 * 中文标签映射 —— 把 AI 事件里的英文代码标识符翻译成新手能看懂的中文。
 *
 * 为什么放这里而不放 i18n JSON:
 * - event_type / severity 是固定枚举,但 class_name 来自 COCO 80 类,放进 i18n JSON
 *   会让字典臃肿;且 t() 对缺失键会返回带点号的 key 字符串(如 "aiEvents.class.chair"),
 *   反而更难读。
 * - 这里统一兜底:任何未覆盖的值原样返回,保证不会显示乱码。
 *
 * 用法:在 AIEvents.svelte 里用 eventTypeLabel(evt.event_type) 等。
 */

// COCO (Common Objects in Context) 80 类完整中文表。
// 顺序与 ai-detection/inference.ts 的 COCO_CLASSES 一致,便于核对。
const COCO_LABELS_ZH: Record<string, string> = {
  person: '人',
  bicycle: '自行车',
  car: '汽车',
  motorcycle: '摩托车',
  airplane: '飞机',
  bus: '公交车',
  train: '火车',
  truck: '卡车',
  boat: '船',
  'traffic light': '交通灯',
  'fire hydrant': '消火栓',
  'stop sign': '停车标志',
  'parking meter': '停车计时器',
  bench: '长椅',
  bird: '鸟',
  cat: '猫',
  dog: '狗',
  horse: '马',
  sheep: '羊',
  cow: '牛',
  elephant: '大象',
  bear: '熊',
  zebra: '斑马',
  giraffe: '长颈鹿',
  backpack: '背包',
  umbrella: '雨伞',
  handbag: '手提包',
  tie: '领带',
  suitcase: '行李箱',
  frisbee: '飞盘',
  skis: '滑雪板(双板)',
  snowboard: '滑雪板(单板)',
  'sports ball': '运动球',
  kite: '风筝',
  'baseball bat': '棒球棒',
  'baseball glove': '棒球手套',
  skateboard: '滑板',
  surfboard: '冲浪板',
  'tennis racket': '网球拍',
  bottle: '瓶子',
  'wine glass': '酒杯',
  cup: '杯子',
  fork: '叉子',
  knife: '刀',
  spoon: '勺子',
  bowl: '碗',
  banana: '香蕉',
  apple: '苹果',
  sandwich: '三明治',
  orange: '橙子',
  broccoli: '西兰花',
  carrot: '胡萝卜',
  'hot dog': '热狗',
  pizza: '披萨',
  donut: '甜甜圈',
  cake: '蛋糕',
  chair: '椅子',
  couch: '沙发',
  'potted plant': '盆栽',
  bed: '床',
  'dining table': '餐桌',
  toilet: '马桶',
  tv: '电视',
  laptop: '笔记本电脑',
  mouse: '鼠标',
  remote: '遥控器',
  keyboard: '键盘',
  'cell phone': '手机',
  microwave: '微波炉',
  oven: '烤箱',
  toaster: '烤面包机',
  sink: '水槽',
  refrigerator: '冰箱',
  book: '书',
  clock: '时钟',
  vase: '花瓶',
  scissors: '剪刀',
  'teddy bear': '泰迪熊',
  'hair drier': '吹风机',
  toothbrush: '牙刷',
};

// 事件类型 → 通俗中文。未知类型原样返回(不硬翻,避免误导)。
const EVENT_TYPE_LABELS_ZH: Record<string, string> = {
  zone_intrusion: '进入区域',
  line_crossing: '越线',
  loitering: '逗留',
  object_detected: '检测到物体',
  custom: '自定义',
};

// 严重度 → 中文。
const SEVERITY_LABELS_ZH: Record<string, string> = {
  info: '提示',
  warning: '警告',
  critical: '严重',
};

// 内置 zone 名 → 中文(仅翻译程序生成的固定 zone,用户自定义的 zone 原样保留)。
const ZONE_LABELS_ZH: Record<string, string> = {
  'full-frame': '全画面',
};

/** 把英文首字母大写,作为未知 event_type 的可读兜底。 */
function titleCase(s: string): string {
  if (!s) return s;
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** 检测物体类别 → 中文;查不到则原样返回英文。空值返回空串。 */
export function classLabel(name?: string): string {
  if (!name) return '';
  return COCO_LABELS_ZH[name] ?? name;
}

/** 事件类型 → 中文;未知类型把首字母大写兜底。空值返回空串。 */
export function eventTypeLabel(type?: string): string {
  if (!type) return '';
  return EVENT_TYPE_LABELS_ZH[type] ?? titleCase(type);
}

/** 严重度 → 中文;未知值原样返回。空值返回空串。 */
export function severityLabel(severity?: string): string {
  if (!severity) return '';
  return SEVERITY_LABELS_ZH[severity] ?? severity;
}

/** zone 名 → 中文;仅翻译内置 zone,用户自定义的 zone 原样返回。空值返回空串。 */
export function zoneLabel(name?: string): string {
  if (!name) return '';
  return ZONE_LABELS_ZH[name] ?? name;
}
